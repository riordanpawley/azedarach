package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"golang.org/x/sys/unix"
)

type IssueFanoutOptions struct {
	Project   string
	InputPath string
	Apply     bool
	JSON      bool
}

type IssueFanoutReadyOptions struct {
	Project     string
	RootIssueID string
	JSON        bool
}

type IssueFanoutDriftOptions struct {
	Project   string
	IssueID   string
	Worktree  string
	JSON      bool
	FailOnOut bool
}

type MailSendOptions struct {
	ParentIssueID string
	IssueID       string
	Type          string
	From          string
	To            string
	Body          string
	JSON          bool
}

type MailListOptions struct {
	ParentIssueID string
	SinceSeq      int64
	Limit         int
	JSON          bool
}

type MailWatchOptions struct {
	ParentIssueID string
	SinceSeq      int64
	JSONL         bool
	Once          bool
}

type fanoutSpec struct {
	ParentIssue string       `json:"parent_issue"`
	Nodes       []fanoutNode `json:"nodes"`
}

type fanoutNode struct {
	Key         string       `json:"key"`
	Kind        string       `json:"kind"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Impl        []string     `json:"impl"`
	FileBudget  []string     `json:"file_budget"`
	DependsOn   []string     `json:"depends_on"`
	Children    []fanoutNode `json:"children"`
}

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

type fanoutPlan struct {
	ParentIssue string             `json:"parent_issue"`
	NodeCount   int                `json:"node_count"`
	Create      []fanoutCreatePlan `json:"create"`
	Blocks      []fanoutBlocksPlan `json:"blocks"`
	Warnings    []string           `json:"warnings,omitempty"`
}

type fanoutCreatePlan struct {
	Key        string   `json:"key"`
	Title      string   `json:"title"`
	Kind       string   `json:"kind"`
	Parent     string   `json:"parent"`
	Type       string   `json:"issue_type"`
	Impl       []string `json:"impl"`
	FileBudget []string `json:"file_budget,omitempty"`
}

type fanoutBlocksPlan struct {
	IssueKey     string `json:"issue_key"`
	DependsOnKey string `json:"depends_on_key"`
	Type         string `json:"type"`
}

type fanoutApplyResult struct {
	ParentIssue string            `json:"parent_issue"`
	Created     map[string]string `json:"created"`
	BlocksAdded int               `json:"blocks_added"`
}

type fanoutRegistryEntry struct {
	IssueID      string   `json:"issue_id"`
	ParentIssue  string   `json:"parent_issue"`
	Key          string   `json:"key"`
	Kind         string   `json:"kind"`
	FileBudget   []string `json:"file_budget,omitempty"`
	CreatedAtUTC string   `json:"created_at_utc"`
}

type fanoutReadyResult struct {
	RootIssueID string            `json:"root_issue_id"`
	Runnable    []string          `json:"runnable"`
	Blocked     map[string]string `json:"blocked"`
}

type driftResult struct {
	IssueID      string   `json:"issue_id"`
	Worktree     string   `json:"worktree"`
	FileBudget   []string `json:"file_budget"`
	ChangedFiles []string `json:"changed_files"`
	OutOfBudget  []string `json:"out_of_budget"`
	AdvisoryOnly bool     `json:"advisory_only"`
}

type mailEvent struct {
	Seq         int64                  `json:"seq"`
	ParentIssue string                 `json:"parent_issue"`
	IssueID     string                 `json:"issue_id,omitempty"`
	Type        string                 `json:"type"`
	From        string                 `json:"from,omitempty"`
	To          string                 `json:"to,omitempty"`
	Body        string                 `json:"body"`
	CreatedAt   time.Time              `json:"created_at"`
	Payload     map[string]interface{} `json:"payload,omitempty"`
}

func ParseIssueFanoutArgs(args []string) (IssueFanoutOptions, error) {
	opts := IssueFanoutOptions{}
	fs := flag.NewFlagSet("issue fanout", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&opts.InputPath, "input", "", "path to fanout spec JSON")
	fs.BoolVar(&opts.Apply, "apply", false, "apply planned operations")
	fs.BoolVar(&opts.JSON, "json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return IssueFanoutOptions{}, err
	}
	if fs.NArg() != 0 {
		return IssueFanoutOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if strings.TrimSpace(opts.InputPath) == "" {
		return IssueFanoutOptions{}, fmt.Errorf("missing required flag: --input")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseIssueFanoutReadyArgs(args []string) (IssueFanoutReadyOptions, error) {
	opts := IssueFanoutReadyOptions{}
	fs := flag.NewFlagSet("issue fanout ready", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&opts.RootIssueID, "root", "", "root issue id")
	fs.BoolVar(&opts.JSON, "json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return IssueFanoutReadyOptions{}, err
	}
	if fs.NArg() != 0 {
		return IssueFanoutReadyOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if strings.TrimSpace(opts.RootIssueID) == "" {
		return IssueFanoutReadyOptions{}, fmt.Errorf("missing required flag: --root")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseIssueFanoutDriftArgs(args []string) (IssueFanoutDriftOptions, error) {
	opts := IssueFanoutDriftOptions{}
	fs := flag.NewFlagSet("issue fanout drift", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&opts.IssueID, "issue", "", "issue id")
	fs.StringVar(&opts.Worktree, "worktree", "", "worktree path (defaults to cwd)")
	fs.BoolVar(&opts.JSON, "json", false, "output JSON")
	fs.BoolVar(&opts.FailOnOut, "fail-on-out", false, "return non-zero if out-of-budget files are detected")
	if err := fs.Parse(args); err != nil {
		return IssueFanoutDriftOptions{}, err
	}
	if fs.NArg() != 0 {
		return IssueFanoutDriftOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if strings.TrimSpace(opts.IssueID) == "" {
		return IssueFanoutDriftOptions{}, fmt.Errorf("missing required flag: --issue")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func IssueFanoutCommand(deps *Dependencies, opts IssueFanoutOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}
	specJSON, err := os.ReadFile(opts.InputPath)
	if err != nil {
		return fmt.Errorf("read fanout spec: %w", err)
	}
	req := protocol.FanoutCommandBody{
		Apply:              opts.Apply,
		RepoDir:            deps.RepoDir,
		DefaultParentIssue: strings.TrimSpace(os.Getenv("AZEDARACH_ISSUE_ID")),
		Spec:               specJSON,
	}
	if !opts.Apply {
		plan, err := deps.DaemonClient.FanoutPlan(ctx, req)
		if err != nil {
			return err
		}
		if opts.JSON {
			return printJSON(plan)
		}
		printFanoutPlan(fanoutPlanFromProtocol(plan))
		return nil
	}

	result, err := deps.DaemonClient.FanoutApply(ctx, req)
	if err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(result)
	}
	fmt.Printf("Fanout applied under %s\n", result.ParentIssue)
	keys := make([]string, 0, len(result.Created))
	for k := range result.Created {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Printf("- %s -> %s\n", key, result.Created[key])
	}
	fmt.Printf("Blocks edges created: %d\n", result.BlocksAdded)
	return nil
}

func IssueFanoutReadyCommand(deps *Dependencies, opts IssueFanoutReadyOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}
	tasks, err := deps.DaemonClient.ListTasks(ctx)
	if err != nil {
		return fmt.Errorf("list tasks: %w", err)
	}
	result, err := computeRunnableLeaves(opts.RootIssueID, tasks)
	if err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(result)
	}
	fmt.Printf("Root issue: %s\n", result.RootIssueID)
	fmt.Println("Runnable leaves:")
	if len(result.Runnable) == 0 {
		fmt.Println("- (none)")
	} else {
		for _, id := range result.Runnable {
			fmt.Printf("- %s\n", id)
		}
	}
	if len(result.Blocked) > 0 {
		fmt.Println("Blocked leaves:")
		ids := make([]string, 0, len(result.Blocked))
		for id := range result.Blocked {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			fmt.Printf("- %s: %s\n", id, result.Blocked[id])
		}
	}
	return nil
}

func IssueFanoutDriftCommand(deps *Dependencies, opts IssueFanoutDriftOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	worktree := strings.TrimSpace(opts.Worktree)
	result, err := deps.DaemonClient.FanoutDrift(ctx, protocol.FanoutDriftCommandBody{
		IssueID:  opts.IssueID,
		RepoDir:  deps.RepoDir,
		Worktree: worktree,
	})
	if err != nil {
		return err
	}
	if opts.JSON {
		if err := printJSON(result); err != nil {
			return err
		}
	} else {
		fmt.Printf("Issue: %s\n", result.IssueID)
		fmt.Printf("Worktree: %s\n", result.Worktree)
		fmt.Printf("Budget patterns: %s\n", strings.Join(result.FileBudget, ", "))
		fmt.Printf("Changed files: %d\n", len(result.ChangedFiles))
		if len(result.OutOfBudget) == 0 {
			fmt.Println("Ownership drift: none")
		} else {
			fmt.Println("Ownership drift (advisory):")
			for _, p := range result.OutOfBudget {
				fmt.Printf("- %s\n", p)
			}
		}
	}
	if opts.FailOnOut && len(result.OutOfBudget) > 0 {
		return fmt.Errorf("out-of-budget files detected (%d)", len(result.OutOfBudget))
	}
	return nil
}

func ParseMailSendArgs(args []string) (MailSendOptions, error) {
	opts := MailSendOptions{}
	fs := flag.NewFlagSet("mail send", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.ParentIssueID, "parent", "", "parent issue id")
	fs.StringVar(&opts.IssueID, "issue", "", "issue id")
	fs.StringVar(&opts.Type, "type", "", "event type")
	fs.StringVar(&opts.From, "from", "", "producer identity")
	fs.StringVar(&opts.To, "to", "", "target identity")
	fs.StringVar(&opts.Body, "body", "", "message body")
	fs.BoolVar(&opts.JSON, "json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return MailSendOptions{}, err
	}
	if fs.NArg() != 0 {
		return MailSendOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if strings.TrimSpace(opts.ParentIssueID) == "" {
		return MailSendOptions{}, fmt.Errorf("missing required flag: --parent")
	}
	if strings.TrimSpace(opts.Type) == "" {
		return MailSendOptions{}, fmt.Errorf("missing required flag: --type")
	}
	if strings.TrimSpace(opts.Body) == "" {
		return MailSendOptions{}, fmt.Errorf("missing required flag: --body")
	}
	return opts, nil
}

func ParseMailListArgs(args []string) (MailListOptions, error) {
	opts := MailListOptions{Limit: 200}
	fs := flag.NewFlagSet("mail list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.ParentIssueID, "parent", "", "parent issue id")
	fs.Int64Var(&opts.SinceSeq, "since", 0, "inclusive sequence cursor")
	fs.IntVar(&opts.Limit, "limit", 200, "maximum events")
	fs.BoolVar(&opts.JSON, "json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return MailListOptions{}, err
	}
	if fs.NArg() != 0 {
		return MailListOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if strings.TrimSpace(opts.ParentIssueID) == "" {
		return MailListOptions{}, fmt.Errorf("missing required flag: --parent")
	}
	if opts.Limit < 1 {
		return MailListOptions{}, fmt.Errorf("limit must be >= 1")
	}
	return opts, nil
}

func ParseMailWatchArgs(args []string) (MailWatchOptions, error) {
	opts := MailWatchOptions{JSONL: true}
	fs := flag.NewFlagSet("mail watch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.ParentIssueID, "parent", "", "parent issue id")
	fs.Int64Var(&opts.SinceSeq, "since", 0, "inclusive sequence cursor")
	fs.BoolVar(&opts.JSONL, "jsonl", true, "emit newline-delimited JSON")
	fs.BoolVar(&opts.Once, "once", false, "read once and exit")
	if err := fs.Parse(args); err != nil {
		return MailWatchOptions{}, err
	}
	if fs.NArg() != 0 {
		return MailWatchOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if strings.TrimSpace(opts.ParentIssueID) == "" {
		return MailWatchOptions{}, fmt.Errorf("missing required flag: --parent")
	}
	return opts, nil
}

func MailSendCommand(deps *Dependencies, opts MailSendOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}
	event, err := deps.DaemonClient.MailSend(ctx, protocol.MailSendCommandBody{
		RepoDir:     deps.RepoDir,
		ParentIssue: opts.ParentIssueID,
		IssueID:     strings.TrimSpace(opts.IssueID),
		Type:        strings.TrimSpace(opts.Type),
		From:        strings.TrimSpace(opts.From),
		To:          strings.TrimSpace(opts.To),
		Body:        opts.Body,
	})
	if err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(event)
	}
	fmt.Printf("mail event seq=%d parent=%s type=%s\n", event.Seq, event.ParentIssue, event.Type)
	return nil
}

func MailListCommand(deps *Dependencies, opts MailListOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}
	events, err := deps.DaemonClient.MailList(ctx, protocol.MailListCommandBody{
		RepoDir:     deps.RepoDir,
		ParentIssue: opts.ParentIssueID,
		SinceSeq:    opts.SinceSeq,
		Limit:       opts.Limit,
	})
	if err != nil {
		return err
	}
	filtered := make([]mailEvent, 0, len(events))
	for _, evt := range events {
		filtered = append(filtered, protocolToLocalMailEvent(evt))
	}
	if opts.JSON {
		return printJSON(filtered)
	}
	for _, evt := range filtered {
		fmt.Printf("%d\t%s\t%s\t%s\n", evt.Seq, evt.CreatedAt.Format(time.RFC3339), evt.Type, evt.Body)
	}
	return nil
}

func MailWatchCommand(deps *Dependencies, opts MailWatchOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	lastSeq := opts.SinceSeq
	emit := func(evt mailEvent) error {
		lastSeq = evt.Seq
		if opts.JSONL {
			data, err := json.Marshal(evt)
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}
		fmt.Printf("%d\t%s\t%s\t%s\n", evt.Seq, evt.CreatedAt.Format(time.RFC3339), evt.Type, evt.Body)
		return nil
	}

	initial, err := deps.DaemonClient.MailWatch(ctx, protocol.MailWatchCommandBody{
		RepoDir:     deps.RepoDir,
		ParentIssue: opts.ParentIssueID,
		SinceSeq:    opts.SinceSeq,
	})
	if err != nil {
		return err
	}
	for _, evt := range initial {
		if err := emit(protocolToLocalMailEvent(evt)); err != nil {
			return err
		}
	}
	if opts.Once {
		return nil
	}

	// Stream mode: no heartbeat frames; emit only when new events exist.
	for {
		time.Sleep(250 * time.Millisecond)
		events, err := deps.DaemonClient.MailWatch(context.Background(), protocol.MailWatchCommandBody{
			RepoDir:     deps.RepoDir,
			ParentIssue: opts.ParentIssueID,
			SinceSeq:    lastSeq + 1,
		})
		if err != nil {
			return err
		}
		for _, evt := range events {
			if err := emit(protocolToLocalMailEvent(evt)); err != nil {
				return err
			}
		}
	}
}

func protocolToLocalMailEvent(evt protocol.MailEvent) mailEvent {
	createdAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(evt.CreatedAt))
	if err != nil {
		createdAt, err = time.Parse(time.RFC3339, strings.TrimSpace(evt.CreatedAt))
		if err != nil {
			createdAt = time.Time{}
		}
	}
	return mailEvent{
		Seq:         evt.Seq,
		ParentIssue: evt.ParentIssue,
		IssueID:     evt.IssueID,
		Type:        evt.Type,
		From:        evt.From,
		To:          evt.To,
		Body:        evt.Body,
		CreatedAt:   createdAt,
		Payload:     evt.Payload,
	}
}

func readFanoutSpec(path string) (fanoutSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return fanoutSpec{}, fmt.Errorf("read fanout spec: %w", err)
	}
	var spec fanoutSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return fanoutSpec{}, fmt.Errorf("parse fanout spec: %w", err)
	}
	if len(spec.Nodes) == 0 {
		return fanoutSpec{}, fmt.Errorf("fanout spec requires at least one node")
	}
	return spec, nil
}

func flattenFanout(spec fanoutSpec) ([]fanoutFlatNode, []string, error) {
	out := make([]fanoutFlatNode, 0, 16)
	warnings := make([]string, 0, 4)
	seen := map[string]struct{}{}
	var walk func(parentKey string, n fanoutNode) error
	walk = func(parentKey string, n fanoutNode) error {
		key := strings.TrimSpace(n.Key)
		if key == "" {
			return fmt.Errorf("fanout node key is required")
		}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate fanout node key: %s", key)
		}
		seen[key] = struct{}{}
		kind := strings.TrimSpace(n.Kind)
		if kind == "" {
			kind = "work"
		}
		if kind != "group" && kind != "work" {
			return fmt.Errorf("invalid node kind for %s: %s", key, kind)
		}
		if strings.TrimSpace(n.Title) == "" {
			return fmt.Errorf("fanout node title is required: %s", key)
		}
		out = append(out, fanoutFlatNode{
			Key:         key,
			Kind:        kind,
			Title:       strings.TrimSpace(n.Title),
			Description: strings.TrimSpace(n.Description),
			Impl:        append([]string(nil), n.Impl...),
			FileBudget:  append([]string(nil), n.FileBudget...),
			DependsOn:   append([]string(nil), n.DependsOn...),
			ParentKey:   parentKey,
		})
		if kind == "work" && len(n.Children) > 0 {
			warnings = append(warnings, fmt.Sprintf("node %s has kind=work and children; children still materialized", key))
		}
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
	for _, node := range out {
		for _, dep := range node.DependsOn {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				continue
			}
			if _, ok := seen[dep]; !ok {
				return nil, nil, fmt.Errorf("node %s depends_on unknown key %s", node.Key, dep)
			}
			if dep == node.Key {
				return nil, nil, fmt.Errorf("node %s cannot depend on itself", node.Key)
			}
		}
	}
	return out, warnings, nil
}

func buildFanoutPlan(parentIssue string, flat []fanoutFlatNode, warnings []string) fanoutPlan {
	create := make([]fanoutCreatePlan, 0, len(flat))
	blocks := make([]fanoutBlocksPlan, 0, len(flat))
	for _, node := range flat {
		parent := parentIssue
		if node.ParentKey != "" {
			parent = node.ParentKey
		}
		issueType := "task"
		if node.Kind == "group" {
			issueType = "epic"
		}
		impl := node.Impl
		impl = normalizeFanoutImplementations(impl)
		create = append(create, fanoutCreatePlan{
			Key:        node.Key,
			Title:      node.Title,
			Kind:       node.Kind,
			Parent:     parent,
			Type:       issueType,
			Impl:       impl,
			FileBudget: node.FileBudget,
		})
		for _, dep := range node.DependsOn {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				continue
			}
			blocks = append(blocks, fanoutBlocksPlan{
				IssueKey:     node.Key,
				DependsOnKey: dep,
				Type:         "blocks",
			})
		}
	}
	return fanoutPlan{
		ParentIssue: parentIssue,
		NodeCount:   len(flat),
		Create:      create,
		Blocks:      blocks,
		Warnings:    warnings,
	}
}

func applyFanoutPlan(ctx context.Context, deps *Dependencies, parentIssue string, flat []fanoutFlatNode) (fanoutApplyResult, error) {
	created := make(map[string]string, len(flat))
	registry := make(map[string]fanoutRegistryEntry, len(flat))
	for _, node := range flat {
		parentID := parentIssue
		if node.ParentKey != "" {
			resolvedParent, ok := created[node.ParentKey]
			if !ok {
				return fanoutApplyResult{}, fmt.Errorf("parent key not resolved yet for %s: %s", node.Key, node.ParentKey)
			}
			parentID = resolvedParent
		}
		impl := node.Impl
		impl = normalizeFanoutImplementations(impl)
		tt := domain.TypeTask
		if node.Kind == "group" {
			tt = domain.TypeEpic
		}
		design := fanoutDesignMetadata(node)
		id, err := deps.DaemonClient.CreateTask(ctx, daemonclient.TaskCreateParams{
			Title:           node.Title,
			Description:     node.Description,
			Type:            tt,
			Priority:        domain.P2,
			Implementations: impl,
			Design:          design,
			ParentID:        &parentID,
		})
		if err != nil {
			return fanoutApplyResult{}, fmt.Errorf("create task for key %s: %w", node.Key, err)
		}
		created[node.Key] = id
		registry[id] = fanoutRegistryEntry{
			IssueID:      id,
			ParentIssue:  parentIssue,
			Key:          node.Key,
			Kind:         node.Kind,
			FileBudget:   append([]string(nil), node.FileBudget...),
			CreatedAtUTC: time.Now().UTC().Format(time.RFC3339),
		}
	}
	blocksAdded := 0
	for _, node := range flat {
		fromID := created[node.Key]
		for _, dep := range node.DependsOn {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				continue
			}
			toID := created[dep]
			if err := deps.DaemonClient.AddTaskDependency(ctx, daemonclient.TaskDependencyParams{
				TaskID:      fromID,
				DependsOnID: toID,
				Type:        string(domain.DependencyBlocks),
			}); err != nil {
				return fanoutApplyResult{}, fmt.Errorf("add blocks edge %s->%s: %w", node.Key, dep, err)
			}
			blocksAdded++
		}
	}
	if err := saveFanoutRegistry(deps.RepoDir, registry); err != nil {
		return fanoutApplyResult{}, err
	}
	return fanoutApplyResult{
		ParentIssue: parentIssue,
		Created:     created,
		BlocksAdded: blocksAdded,
	}, nil
}

func normalizeFanoutImplementations(impls []string) []string {
	if len(impls) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(impls))
	out := make([]string, 0, len(impls))
	for _, impl := range impls {
		value := strings.TrimSpace(impl)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

func printFanoutPlan(plan fanoutPlan) {
	fmt.Printf("Parent issue: %s\n", plan.ParentIssue)
	fmt.Printf("Nodes: %d\n", plan.NodeCount)
	fmt.Println("Create operations:")
	for _, c := range plan.Create {
		fmt.Printf("- %s [%s] %s parent=%s impl=%s\n", c.Key, c.Kind, c.Title, c.Parent, strings.Join(c.Impl, ","))
	}
	if len(plan.Blocks) > 0 {
		fmt.Println("Blocks dependencies:")
		for _, b := range plan.Blocks {
			fmt.Printf("- %s blocks %s\n", b.IssueKey, b.DependsOnKey)
		}
	}
	if len(plan.Warnings) > 0 {
		fmt.Println("Warnings:")
		for _, w := range plan.Warnings {
			fmt.Printf("- %s\n", w)
		}
	}
}

func fanoutPlanFromProtocol(plan protocol.FanoutPlan) fanoutPlan {
	create := make([]fanoutCreatePlan, 0, len(plan.Create))
	for _, item := range plan.Create {
		create = append(create, fanoutCreatePlan{
			Key:        item.Key,
			Title:      item.Title,
			Kind:       item.Kind,
			Parent:     item.Parent,
			Type:       item.Type,
			Impl:       item.Impl,
			FileBudget: item.FileBudget,
		})
	}
	blocks := make([]fanoutBlocksPlan, 0, len(plan.Blocks))
	for _, item := range plan.Blocks {
		blocks = append(blocks, fanoutBlocksPlan{
			IssueKey:     item.IssueKey,
			DependsOnKey: item.DependsOnKey,
			Type:         item.Type,
		})
	}
	return fanoutPlan{
		ParentIssue: plan.ParentIssue,
		NodeCount:   plan.NodeCount,
		Create:      create,
		Blocks:      blocks,
		Warnings:    plan.Warnings,
	}
}

func computeRunnableLeaves(rootIssueID string, tasks []domain.Task) (fanoutReadyResult, error) {
	byID := make(map[string]domain.Task, len(tasks))
	children := make(map[string][]string, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
		if t.ParentID != nil && strings.TrimSpace(*t.ParentID) != "" {
			p := strings.TrimSpace(*t.ParentID)
			children[p] = append(children[p], t.ID)
		}
	}
	if _, ok := byID[rootIssueID]; !ok {
		return fanoutReadyResult{}, fmt.Errorf("root issue not found: %s", rootIssueID)
	}
	desc := collectDescendants(rootIssueID, children)
	leaves := make([]string, 0, len(desc))
	for _, id := range desc {
		task := byID[id]
		if task.Type == domain.TypeEpic {
			continue
		}
		if len(children[id]) == 0 {
			leaves = append(leaves, id)
		}
	}
	sort.Strings(leaves)
	result := fanoutReadyResult{
		RootIssueID: rootIssueID,
		Runnable:    make([]string, 0, len(leaves)),
		Blocked:     make(map[string]string),
	}
	for _, id := range leaves {
		task := byID[id]
		if task.Status == domain.StatusDone {
			continue
		}
		if task.Status == domain.StatusBlocked {
			result.Blocked[id] = "status=blocked"
			continue
		}
		blockers := unresolvedBlockers(task, byID)
		if len(blockers) > 0 {
			result.Blocked[id] = "waiting on " + strings.Join(blockers, ",")
			continue
		}
		result.Runnable = append(result.Runnable, id)
	}
	return result, nil
}

func collectDescendants(root string, children map[string][]string) []string {
	out := make([]string, 0, 16)
	stack := append([]string(nil), children[root]...)
	seen := map[string]struct{}{}
	for len(stack) > 0 {
		cur := stack[0]
		stack = stack[1:]
		if _, ok := seen[cur]; ok {
			continue
		}
		seen[cur] = struct{}{}
		out = append(out, cur)
		stack = append(stack, children[cur]...)
	}
	return out
}

func unresolvedBlockers(task domain.Task, byID map[string]domain.Task) []string {
	out := make([]string, 0, 4)
	for _, dep := range task.Dependencies {
		if dep.Type != domain.DependencyBlocks {
			continue
		}
		depTask, ok := byID[dep.ID]
		if !ok {
			out = append(out, dep.ID+"(missing)")
			continue
		}
		if depTask.Status != domain.StatusDone {
			out = append(out, dep.ID)
		}
	}
	sort.Strings(out)
	return out
}

func fanoutDesignMetadata(node fanoutFlatNode) string {
	budgets := strings.Join(node.FileBudget, ",")
	return fmt.Sprintf("fanout.key=%s\nfanout.kind=%s\nfanout.file_budget=%s", node.Key, node.Kind, budgets)
}

func gitChangedFiles(worktree string) ([]string, error) {
	cmds := [][]string{
		{"diff", "--name-only"},
		{"diff", "--name-only", "--cached"},
		{"ls-files", "--others", "--exclude-standard"},
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, 16)
	for _, args := range cmds {
		cmd := newGitCommand(worktree, args...)
		output, err := cmd.Output()
		if err != nil {
			return nil, err
		}
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			line = filepath.ToSlash(line)
			if _, ok := seen[line]; ok {
				continue
			}
			seen[line] = struct{}{}
			out = append(out, line)
		}
	}
	sort.Strings(out)
	return out, nil
}

func lockMailbox(repoDir, parentIssue string) (func(), error) {
	path := mailboxLockPath(repoDir, parentIssue)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create mailbox lock dir: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open mailbox lock file: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("acquire mailbox lock: %w", err)
	}
	released := false
	return func() {
		if released {
			return
		}
		released = true
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}, nil
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
			// Basic support for ** by prefix fallback.
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

func mailboxPath(repoDir, parentIssue string) string {
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, strings.TrimSpace(parentIssue))
	return filepath.Join(repoDir, ".azedarach", "mailbox", safe+".jsonl")
}

func mailboxLockPath(repoDir, parentIssue string) string {
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, strings.TrimSpace(parentIssue))
	return filepath.Join(repoDir, ".azedarach", "mailbox", safe+".lock")
}

func readMailboxEvents(repoDir, parentIssue string) ([]mailEvent, error) {
	path := mailboxPath(repoDir, parentIssue)
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open mailbox: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	out := make([]mailEvent, 0, 64)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var evt mailEvent
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			return nil, fmt.Errorf("decode mailbox event: %w", err)
		}
		out = append(out, evt)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan mailbox: %w", err)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

func appendMailboxEvent(repoDir string, evt mailEvent) error {
	path := mailboxPath(repoDir, evt.ParentIssue)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create mailbox dir: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open mailbox file: %w", err)
	}
	defer file.Close()
	data, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("encode mailbox event: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write mailbox event: %w", err)
	}
	return nil
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
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write fanout registry: %w", err)
	}
	return nil
}
