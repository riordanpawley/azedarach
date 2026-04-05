package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

type fanoutFlatNode struct {
	Key         string
	Kind        string
	Title       string
	Description string
	Impl        []string
	FileBudget  []string
	DependsOn   []string
	ParentKey   string
}

type fanoutRegistryEntry struct {
	IssueID      string   `json:"issue_id"`
	ParentIssue  string   `json:"parent_issue"`
	Key          string   `json:"key"`
	Kind         string   `json:"kind"`
	FileBudget   []string `json:"file_budget,omitempty"`
	CreatedAtUTC string   `json:"created_at_utc"`
}

func (d *Daemon) handleIssueFanout(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var cmd protocol.FanoutCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	repoDir := strings.TrimSpace(cmd.RepoDir)
	if repoDir == "" {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "missing required field: repo_dir"), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon fanout requested", "repo_dir", repoDir, "apply", cmd.Apply)
	}
	spec, err := parseFanoutSpec(cmd.Spec)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
	}
	flat, warnings, err := flattenFanout(spec)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
	}
	parentIssue := strings.TrimSpace(spec.ParentIssue)
	if parentIssue == "" {
		parentIssue = strings.TrimSpace(cmd.DefaultParentIssue)
	}
	if parentIssue == "" {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "fanout requires parent_issue in spec or default_parent_issue"), nil
	}

	resp := d.successResponse(req)
	if !cmd.Apply {
		plan := buildFanoutPlan(parentIssue, flat, warnings)
		body, err := json.Marshal(plan)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		resp.Body = body
		if d.cfg.Logger != nil {
			d.cfg.Logger.Info("daemon fanout plan completed",
				"repo_dir", repoDir,
				"parent_issue", parentIssue,
				"node_count", len(flat),
				"warning_count", len(warnings),
			)
		}
		return resp, nil
	}

	result, err := d.applyFanoutPlan(ctx, parentIssue, flat, repoDir)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	body, err := json.Marshal(result)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp.Body = body
	resp.Revision = d.nextRevision(d.projectID(req.Meta))
	d.publishTaskEvent(req, "task.updated", resp.Revision)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon fanout apply completed",
			"repo_dir", repoDir,
			"parent_issue", result.ParentIssue,
			"created_count", len(result.Created),
			"blocks_added", result.BlocksAdded,
			"revision", resp.Revision,
		)
	}
	return resp, nil
}

func (d *Daemon) handleIssueFanoutDrift(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var cmd protocol.FanoutDriftCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	repoDir := strings.TrimSpace(cmd.RepoDir)
	if repoDir == "" {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "missing required field: repo_dir"), nil
	}
	issueID := strings.TrimSpace(cmd.IssueID.String())
	if issueID == "" {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "missing required field: issue_id"), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon fanout drift requested", "repo_dir", repoDir, "issue_id", issueID, "worktree", strings.TrimSpace(cmd.Worktree))
	}
	registry, err := loadFanoutRegistry(repoDir)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	entry, ok := registry[issueID]
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("no fanout registry entry for issue %s", issueID)), nil
	}
	worktree := strings.TrimSpace(cmd.Worktree)
	if worktree == "" {
		worktree = repoDir
	}
	projectID := d.projectID(req.Meta)
	changed, err := d.fanoutDriftChangedFilesFromProjection(ctx, projectID, issueID, worktree)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("projection changed files: %v", err)), nil
	}
	result := protocol.FanoutDriftResult{
		IssueID:      cmd.IssueID,
		Worktree:     worktree,
		FileBudget:   entry.FileBudget,
		ChangedFiles: changed,
		OutOfBudget:  outOfBudgetFiles(changed, entry.FileBudget),
		AdvisoryOnly: true,
	}
	body, err := json.Marshal(result)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon fanout drift completed",
			"repo_dir", repoDir,
			"issue_id", issueID,
			"changed_count", len(changed),
			"out_of_budget_count", len(result.OutOfBudget),
		)
	}
	return resp, nil
}

func parseFanoutSpec(data []byte) (protocol.FanoutSpec, error) {
	if len(data) == 0 {
		return protocol.FanoutSpec{}, fmt.Errorf("fanout spec is required")
	}
	var spec protocol.FanoutSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return protocol.FanoutSpec{}, fmt.Errorf("parse fanout spec: %w", err)
	}
	if len(spec.Nodes) == 0 {
		return protocol.FanoutSpec{}, fmt.Errorf("fanout spec requires at least one node")
	}
	return spec, nil
}

func flattenFanout(spec protocol.FanoutSpec) ([]fanoutFlatNode, []string, error) {
	out := make([]fanoutFlatNode, 0, 16)
	seen := map[string]struct{}{}
	warnings := make([]string, 0, 4)
	var walk func(parentKey string, n protocol.FanoutNode) error
	walk = func(parentKey string, n protocol.FanoutNode) error {
		key := strings.TrimSpace(n.Key)
		if key == "" {
			return fmt.Errorf("fanout node key is required")
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate fanout node key: %s", key)
		}
		seen[key] = struct{}{}
		kind := strings.TrimSpace(n.Kind)
		if kind == "" {
			kind = "work"
		}
		if kind == "work" && len(n.Children) > 0 {
			warnings = append(warnings, fmt.Sprintf("node %s declares kind=work and children; treating as group boundary", key))
		}
		title := strings.TrimSpace(n.Title)
		if title == "" {
			return fmt.Errorf("fanout node title is required: %s", key)
		}
		out = append(out, fanoutFlatNode{
			Key:         key,
			Kind:        kind,
			Title:       title,
			Description: strings.TrimSpace(n.Description),
			Impl:        normalizeImpls(n.Impl),
			FileBudget:  normalizePatterns(n.FileBudget),
			DependsOn:   normalizeDeps(n.DependsOn),
			ParentKey:   strings.TrimSpace(parentKey),
		})
		for _, child := range n.Children {
			if err := walk(key, child); err != nil {
				return err
			}
		}
		return nil
	}
	for _, node := range spec.Nodes {
		if err := walk("", node); err != nil {
			return nil, nil, err
		}
	}
	keys := make(map[string]struct{}, len(out))
	for _, node := range out {
		keys[node.Key] = struct{}{}
	}
	for _, node := range out {
		for _, dep := range node.DependsOn {
			if _, ok := keys[dep]; !ok {
				return nil, nil, fmt.Errorf("node %s depends_on unknown key %s", node.Key, dep)
			}
		}
	}
	return out, warnings, nil
}

func buildFanoutPlan(parentIssue string, flat []fanoutFlatNode, warnings []string) protocol.FanoutPlan {
	create := make([]protocol.FanoutCreatePlan, 0, len(flat))
	blocks := make([]protocol.FanoutBlocksPlan, 0, len(flat))
	for _, node := range flat {
		issueType := "task"
		switch node.Kind {
		case "group":
			issueType = "epic"
		case "bug", "feature", "chore", "task", "epic":
			issueType = node.Kind
		}
		create = append(create, protocol.FanoutCreatePlan{
			Key:        node.Key,
			Title:      node.Title,
			Kind:       node.Kind,
			Parent:     pickParent(node.ParentKey, parentIssue),
			Type:       issueType,
			Impl:       node.Impl,
			FileBudget: node.FileBudget,
		})
		for _, dep := range node.DependsOn {
			blocks = append(blocks, protocol.FanoutBlocksPlan{
				IssueKey:     node.Key,
				DependsOnKey: dep,
				Type:         "blocks",
			})
		}
	}
	return protocol.FanoutPlan{
		ParentIssue: parentIssue,
		NodeCount:   len(flat),
		Create:      create,
		Blocks:      blocks,
		Warnings:    warnings,
	}
}

func (d *Daemon) applyFanoutPlan(ctx context.Context, parentIssue string, flat []fanoutFlatNode, repoDir string) (protocol.FanoutApplyResult, error) {
	issueClient := d.issueClientForProject(daemonProjectIDFromContext(ctx))
	if issueClient == nil {
		return protocol.FanoutApplyResult{}, fmt.Errorf("issue store unavailable")
	}
	created := make(map[string]string, len(flat))
	registry := make(map[string]fanoutRegistryEntry, len(flat))
	blocksAdded := 0
	for _, node := range flat {
		parentID := parentIssue
		if node.ParentKey != "" {
			id, ok := created[node.ParentKey]
			if !ok {
				return protocol.FanoutApplyResult{}, fmt.Errorf("parent key not resolved yet for %s: %s", node.Key, node.ParentKey)
			}
			parentID = id
		}
		priority := domain.P2
		taskType := mapKindToTaskType(node.Kind)
		estimate := 0
		notes := fmt.Sprintf("fanout.key=%s\nfanout.parent=%s", node.Key, node.ParentKey)
		design := fanoutDesignMetadata(node)
		id, err := issueClient.Create(ctx, issues.CreateTaskParams{
			Title:           node.Title,
			Description:     node.Description,
			Type:            taskType,
			Priority:        priority,
			Status:          domain.StatusOpen,
			Implementations: node.Impl,
			Design:          design,
			Notes:           notes,
			Estimate:        &estimate,
			ParentID:        &parentID,
		})
		if err != nil {
			return protocol.FanoutApplyResult{}, fmt.Errorf("create task for key %s: %w", node.Key, err)
		}
		created[node.Key] = id
		registry[id] = fanoutRegistryEntry{
			IssueID:      id,
			ParentIssue:  parentIssue,
			Key:          node.Key,
			Kind:         node.Kind,
			FileBudget:   node.FileBudget,
			CreatedAtUTC: time.Now().UTC().Format(time.RFC3339),
		}
	}
	for _, node := range flat {
		issueID := created[node.Key]
		for _, dep := range node.DependsOn {
			depID := created[dep]
			if depID == "" {
				return protocol.FanoutApplyResult{}, fmt.Errorf("depends_on key unresolved for %s: %s", node.Key, dep)
			}
			if err := issueClient.AddDependency(ctx, issueID, depID, string(domain.DependencyBlocks)); err != nil {
				return protocol.FanoutApplyResult{}, fmt.Errorf("add blocks edge %s->%s: %w", node.Key, dep, err)
			}
			blocksAdded++
		}
	}
	if err := saveFanoutRegistry(repoDir, registry); err != nil {
		return protocol.FanoutApplyResult{}, err
	}
	return protocol.FanoutApplyResult{
		ParentIssue: parentIssue,
		Created:     created,
		BlocksAdded: blocksAdded,
	}, nil
}

func pickParent(parentKey, parentIssue string) string {
	if strings.TrimSpace(parentKey) == "" {
		return parentIssue
	}
	return parentKey
}

func mapKindToTaskType(kind string) domain.TaskType {
	switch strings.TrimSpace(kind) {
	case "group", "epic":
		return domain.TypeEpic
	case "feature":
		return domain.TypeFeature
	case "bug":
		return domain.TypeBug
	case "chore":
		return domain.TypeChore
	default:
		return domain.TypeTask
	}
}

func fanoutDesignMetadata(node fanoutFlatNode) string {
	budgets := strings.Join(node.FileBudget, ",")
	return fmt.Sprintf("fanout.key=%s\nfanout.kind=%s\nfanout.file_budget=%s", node.Key, node.Kind, budgets)
}

func normalizeImpls(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

func normalizePatterns(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = filepath.ToSlash(strings.TrimSpace(v))
		if v == "" {
			continue
		}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func normalizeDeps(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func (d *Daemon) fanoutDriftChangedFilesFromProjection(ctx context.Context, projectID, issueID, worktree string) ([]string, error) {
	store := d.worktreeRuntimeStateStore(projectID)
	if store == nil {
		return []string{}, nil
	}

	projectID = protocol.NormalizeProjectID(projectID)
	issueID = strings.TrimSpace(issueID)
	worktree = strings.TrimSpace(worktree)

	var (
		projection daemonstate.WorktreeState
		found      bool
		err        error
	)
	if issueID != "" {
		projection, found, err = store.GetWorktreeStateByIssueID(ctx, projectID, issueID)
	}
	if err != nil {
		return nil, err
	}
	if !found && worktree != "" {
		projection, found, err = store.GetWorktreeStateByPath(ctx, projectID, worktree)
		if err != nil {
			return nil, err
		}
	}
	if !found || len(projection.GitStatusRaw) == 0 {
		return []string{}, nil
	}

	var status git.GitStatus
	if err := json.Unmarshal(projection.GitStatusRaw, &status); err != nil {
		return nil, fmt.Errorf("unmarshal projection git status: %w", err)
	}

	seen := map[string]struct{}{}
	out := make([]string, 0, len(status.Modified)+len(status.Added)+len(status.Deleted)+len(status.Untracked)+len(status.Staged))
	add := func(paths []string) {
		for _, path := range paths {
			path = filepath.ToSlash(strings.TrimSpace(path))
			if path == "" {
				continue
			}
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			out = append(out, path)
		}
	}
	add(status.Modified)
	add(status.Added)
	add(status.Deleted)
	add(status.Untracked)
	add(status.Staged)
	sort.Strings(out)
	return out, nil
}

func outOfBudgetFiles(paths, budget []string) []string {
	if len(budget) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		ok := false
		for _, pattern := range budget {
			matched, _ := filepath.Match(pattern, p)
			if matched {
				ok = true
				break
			}
			if strings.HasSuffix(pattern, "/**") {
				prefix := strings.TrimSuffix(pattern, "/**")
				if strings.HasPrefix(p, prefix+"/") || p == prefix {
					ok = true
					break
				}
			}
		}
		if !ok {
			out = append(out, p)
		}
	}
	return out
}

func fanoutRegistryPath(repoDir string) string {
	return filepath.Join(repoDir, ".azedarach", "fanout", "registry.json")
}

func loadFanoutRegistry(repoDir string) (map[string]fanoutRegistryEntry, error) {
	path := fanoutRegistryPath(repoDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]fanoutRegistryEntry{}, nil
		}
		return nil, fmt.Errorf("read fanout registry: %w", err)
	}
	out := map[string]fanoutRegistryEntry{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode fanout registry: %w", err)
	}
	return out, nil
}

func saveFanoutRegistry(repoDir string, updates map[string]fanoutRegistryEntry) error {
	existing, err := loadFanoutRegistry(repoDir)
	if err != nil {
		return err
	}
	for issueID, entry := range updates {
		existing[issueID] = entry
	}
	path := fanoutRegistryPath(repoDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create fanout registry dir: %w", err)
	}
	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return fmt.Errorf("encode fanout registry: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write fanout registry: %w", err)
	}
	return nil
}
