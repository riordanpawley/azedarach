package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/cli"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/domain"
)

const (
	showCommandName = "show"
	showUsage       = "Usage: az show <issue-id> [--deps=none|counts|direct|verbose] [--dep-depth <n>] [--dep-type <types>] [--dep-limit <n>] [--dep-node-limit <n>] [--json]"

	showInvalidArgumentCode = "invalid_argument"
	showIssueNotFoundCode   = "issue_not_found"
	showInternalErrorCode   = "internal_error"

	showInvalidArgRemediation = "Run az show <issue-id> [--deps=none|counts|direct|verbose] [--dep-depth <n>] [--dep-type <types>] [--dep-limit <n>] [--dep-node-limit <n>] [--json]"
	showNotFoundRemediation   = "Run az list --json to inspect available issues"
	showInternalRemediation   = "Retry the command and inspect stderr diagnostics"

	showIssueNotFoundMessageTemplate = "Issue not found internally nor externally: %s"

	showExitCodeInvalidUsage = 2
	showExitCodeNotFound     = 3

	showDepsModeNone    = "none"
	showDepsModeCounts  = "counts"
	showDepsModeDirect  = "direct"
	showDepsModeVerbose = "verbose"
)

var showCommandPath = []string{"az", "show"}

type showSearchFunc func(issueID string) ([]domain.Task, error)
type showSearcherFactory func() showSearchFunc

var newShowSearcher showSearcherFactory = defaultShowSearcherFactory

type showParsedArgs struct {
	IssueID      string
	JSONMode     bool
	DepsMode     string
	DepDepth     int
	DepTypes     []string
	DepLimit     int
	DepNodeLimit int
}

type showResult struct {
	ID           string                  `json:"id"`
	Title        string                  `json:"title"`
	Status       string                  `json:"status"`
	Priority     string                  `json:"priority"`
	Type         string                  `json:"type"`
	Dependencies *showDependencyEnvelope `json:"dependencies,omitempty"`
}

type showDependencyEnvelope struct {
	Mode       string                          `json:"mode"`
	Counts     map[string]int                  `json:"counts,omitempty"`
	Direct     map[string][]showDependencyItem `json:"direct,omitempty"`
	Truncation *showDependencyTruncation       `json:"truncation,omitempty"`
}

type showDependencyItem struct {
	ID          string `json:"id"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status,omitempty"`
	Priority    string `json:"priority,omitempty"`
	Type        string `json:"type,omitempty"`
}

type showDependencyTruncation struct {
	Applied       bool `json:"applied"`
	RelationLimit int  `json:"relationLimit,omitempty"`
	NodeLimit     int  `json:"nodeLimit,omitempty"`
	Omitted       int  `json:"omitted"`
}

func handleShowCommand(args []string, stdout, stderr io.Writer) int {
	startedAt := time.Now()

	parsedArgs, parseErr := parseShowArgs(args)
	if parseErr != nil {
		return handleShowInvalidUsage(
			parseErr,
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	searcher := newShowSearcher()
	if searcher == nil {
		return handleShowInternalError(
			fmt.Errorf("show searcher not configured"),
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	tasks, err := searcher(parsedArgs.IssueID)
	if err != nil {
		return handleShowInternalError(
			err,
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	task, found := findShowTaskByID(tasks, parsedArgs.IssueID)
	if !found {
		notFoundMessage := fmt.Sprintf(showIssueNotFoundMessageTemplate, parsedArgs.IssueID)
		notFoundError := NewH1Error(
			showIssueNotFoundCode,
			notFoundMessage,
			showNotFoundRemediation,
			map[string]any{"issueId": parsedArgs.IssueID},
		)

		if parsedArgs.JSONMode {
			envelope := NewH2FailureEnvelope(
				showCommandName,
				showCommandPath,
				showProjectContext(),
				showExecutionMeta(startedAt),
				notFoundError,
			)
			if err := writeJSON(stdout, envelope); err != nil {
				fmt.Fprintf(stderr, "Error: %v\n", err)
				return 1
			}
			return showExitCodeNotFound
		}

		fmt.Fprintln(stderr, notFoundMessage)
		return showExitCodeNotFound
	}

	result := taskToShowResult(task, tasks, parsedArgs)
	if parsedArgs.JSONMode {
		envelope := NewH2SuccessEnvelope(
			showCommandName,
			showCommandPath,
			showProjectContext(),
			showExecutionMeta(startedAt),
			result,
		)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintf(
		stdout,
		"%s %s %s %s %s\n",
		result.ID,
		result.Status,
		result.Priority,
		result.Type,
		result.Title,
	)
	return 0
}

func parseShowArgs(args []string) (showParsedArgs, error) {
	parsed := showParsedArgs{
		JSONMode:     showHasJSONFlag(args),
		DepsMode:     showDepsModeCounts,
		DepDepth:     1,
		DepLimit:     0,
		DepNodeLimit: 0,
	}

	issueIDs := make([]string, 0, 1)
	unknownFlags := make([]string, 0)

	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			parsed.JSONMode = true
		case arg == "--deps":
			if index+1 >= len(args) {
				return parsed, fmt.Errorf("missing value for --deps")
			}
			mode, err := normalizeShowDepsMode(args[index+1])
			if err != nil {
				return parsed, err
			}
			parsed.DepsMode = mode
			index++
		case strings.HasPrefix(arg, "--deps="):
			mode, err := normalizeShowDepsMode(strings.TrimPrefix(arg, "--deps="))
			if err != nil {
				return parsed, err
			}
			parsed.DepsMode = mode
		case arg == "--dep-depth":
			if index+1 >= len(args) {
				return parsed, fmt.Errorf("missing value for --dep-depth")
			}
			depth, err := parseShowNonNegativeInt(args[index+1], "--dep-depth")
			if err != nil {
				return parsed, err
			}
			parsed.DepDepth = depth
			index++
		case strings.HasPrefix(arg, "--dep-depth="):
			depth, err := parseShowNonNegativeInt(strings.TrimPrefix(arg, "--dep-depth="), "--dep-depth")
			if err != nil {
				return parsed, err
			}
			parsed.DepDepth = depth
		case arg == "--dep-type":
			if index+1 >= len(args) {
				return parsed, fmt.Errorf("missing value for --dep-type")
			}
			types, err := parseShowDepTypeList(args[index+1])
			if err != nil {
				return parsed, err
			}
			parsed.DepTypes = mergeUniqueStrings(parsed.DepTypes, types)
			index++
		case strings.HasPrefix(arg, "--dep-type="):
			types, err := parseShowDepTypeList(strings.TrimPrefix(arg, "--dep-type="))
			if err != nil {
				return parsed, err
			}
			parsed.DepTypes = mergeUniqueStrings(parsed.DepTypes, types)
		case arg == "--dep-limit":
			if index+1 >= len(args) {
				return parsed, fmt.Errorf("missing value for --dep-limit")
			}
			limit, err := parseShowNonNegativeInt(args[index+1], "--dep-limit")
			if err != nil {
				return parsed, err
			}
			parsed.DepLimit = limit
			index++
		case strings.HasPrefix(arg, "--dep-limit="):
			limit, err := parseShowNonNegativeInt(strings.TrimPrefix(arg, "--dep-limit="), "--dep-limit")
			if err != nil {
				return parsed, err
			}
			parsed.DepLimit = limit
		case arg == "--dep-node-limit":
			if index+1 >= len(args) {
				return parsed, fmt.Errorf("missing value for --dep-node-limit")
			}
			limit, err := parseShowNonNegativeInt(args[index+1], "--dep-node-limit")
			if err != nil {
				return parsed, err
			}
			parsed.DepNodeLimit = limit
			index++
		case strings.HasPrefix(arg, "--dep-node-limit="):
			limit, err := parseShowNonNegativeInt(strings.TrimPrefix(arg, "--dep-node-limit="), "--dep-node-limit")
			if err != nil {
				return parsed, err
			}
			parsed.DepNodeLimit = limit
		case strings.HasPrefix(arg, "-"):
			unknownFlags = append(unknownFlags, arg)
		default:
			issueIDs = append(issueIDs, arg)
		}
	}

	if len(unknownFlags) > 0 {
		return parsed, fmt.Errorf("unknown flag(s): %s", strings.Join(unknownFlags, " "))
	}
	if len(issueIDs) == 0 {
		return parsed, fmt.Errorf("missing required issue-id")
	}
	if len(issueIDs) > 1 {
		return parsed, fmt.Errorf("expected exactly one issue-id, got %d", len(issueIDs))
	}

	parsed.IssueID = issueIDs[0]
	sort.Strings(parsed.DepTypes)
	return parsed, nil
}

func showHasJSONFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--json" {
			return true
		}
	}
	return false
}

func normalizeShowDepsMode(value string) (string, error) {
	mode := strings.TrimSpace(strings.ToLower(value))
	switch mode {
	case showDepsModeNone, showDepsModeCounts, showDepsModeDirect, showDepsModeVerbose:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported --deps mode: %s", value)
	}
}

func parseShowNonNegativeInt(raw string, flag string) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer >= 0", flag)
	}
	if value < 0 {
		return 0, fmt.Errorf("%s must be >= 0", flag)
	}
	return value, nil
}

func parseShowDepTypeList(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		normalized, err := normalizeShowDependencyType(part)
		if err != nil {
			return nil, err
		}
		if normalized == "" {
			continue
		}
		result = append(result, normalized)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("--dep-type requires at least one relation type")
	}
	return result, nil
}

func normalizeShowDependencyType(raw string) (string, error) {
	value := strings.TrimSpace(strings.ToLower(raw))
	value = strings.ReplaceAll(value, "_", "-")
	switch value {
	case "", ",":
		return "", nil
	case "blocks", "blocking":
		return "blocking", nil
	case "blocked-by", "depends-on", "blockedby":
		return "blocked-by", nil
	case "parent-child", "parentchild":
		return "parent-child", nil
	case "discovered-from", "discoveredfrom":
		return "discovered-from", nil
	case "related", "related-to":
		return "related", nil
	default:
		return "", fmt.Errorf("unsupported --dep-type value: %s", raw)
	}
}

func mergeUniqueStrings(existing []string, incoming []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	result := make([]string, 0, len(existing)+len(incoming))
	for _, item := range existing {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	for _, item := range incoming {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
}

func defaultShowSearcherFactory() showSearchFunc {
	cfg, err := loadConfig()
	if err != nil {
		return func(_ string) ([]domain.Task, error) {
			return nil, fmt.Errorf("failed to load config: %w", err)
		}
	}

	deps, err := cli.NewDependencies(cfg)
	if err != nil {
		return func(_ string) ([]domain.Task, error) {
			return nil, fmt.Errorf("failed to initialize dependencies: %w", err)
		}
	}

	return func(issueID string) ([]domain.Task, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return deps.BeadsClient.Search(ctx, issueID)
	}
}

func findShowTaskByID(tasks []domain.Task, issueID string) (domain.Task, bool) {
	for _, task := range tasks {
		if strings.EqualFold(task.ID, issueID) {
			return task, true
		}
	}
	return domain.Task{}, false
}

func taskToShowResult(task domain.Task, tasks []domain.Task, args showParsedArgs) showResult {
	result := showResult{
		ID:       task.ID,
		Title:    task.Title,
		Status:   string(task.Status),
		Priority: showPriorityString(task.Priority),
		Type:     string(task.Type),
	}

	if args.DepsMode != showDepsModeNone {
		projection := buildShowDependencyProjection(task, tasks, args)
		result.Dependencies = &projection
	}

	return result
}

func buildShowDependencyProjection(task domain.Task, tasks []domain.Task, args showParsedArgs) showDependencyEnvelope {
	counts := make(map[string]int)
	if len(task.Dependencies) == 0 {
		return showDependencyEnvelope{Mode: args.DepsMode, Counts: counts, Direct: map[string][]showDependencyItem{}}
	}

	relatedByID := make(map[string]domain.Task, len(tasks))
	for _, candidate := range tasks {
		relatedByID[candidate.ID] = candidate
	}

	allowedType := make(map[string]struct{}, len(args.DepTypes))
	for _, depType := range args.DepTypes {
		allowedType[depType] = struct{}{}
	}

	type typedDependency struct {
		Relation string
		ID       string
		Item     showDependencyItem
	}
	directEdges := make([]typedDependency, 0, len(task.Dependencies))

	for _, dep := range task.Dependencies {
		relation := canonicalShowDependencyType(dep.Type)
		if len(allowedType) > 0 {
			if _, ok := allowedType[relation]; !ok {
				continue
			}
		}
		counts[relation]++

		if args.DepsMode == showDepsModeDirect || args.DepsMode == showDepsModeVerbose {
			record := showDependencyItem{ID: dep.ID}
			if related, ok := relatedByID[dep.ID]; ok {
				record.Title = related.Title
				if args.DepsMode == showDepsModeVerbose {
					record.Description = related.Description
					record.Status = string(related.Status)
					record.Priority = showPriorityString(related.Priority)
					record.Type = string(related.Type)
				}
			}
			directEdges = append(directEdges, typedDependency{Relation: relation, ID: dep.ID, Item: record})
		}
	}

	sort.SliceStable(directEdges, func(i, j int) bool {
		if directEdges[i].Relation == directEdges[j].Relation {
			return strings.ToLower(directEdges[i].ID) < strings.ToLower(directEdges[j].ID)
		}
		return directEdges[i].Relation < directEdges[j].Relation
	})

	projection := showDependencyEnvelope{Mode: args.DepsMode, Counts: counts}
	if args.DepsMode == showDepsModeCounts {
		return projection
	}

	if args.DepsMode == showDepsModeDirect || args.DepsMode == showDepsModeVerbose {
		direct := make(map[string][]showDependencyItem)
		if args.DepDepth > 0 {
			for _, edge := range directEdges {
				direct[edge.Relation] = append(direct[edge.Relation], edge.Item)
			}
		}
		limited, truncation := applyShowDependencyLimits(direct, args.DepLimit, args.DepNodeLimit)
		projection.Direct = limited
		projection.Truncation = truncation
	}

	return projection
}

func applyShowDependencyLimits(
	groups map[string][]showDependencyItem,
	relationLimit int,
	nodeLimit int,
) (map[string][]showDependencyItem, *showDependencyTruncation) {
	if len(groups) == 0 {
		return map[string][]showDependencyItem{}, nil
	}
	if relationLimit <= 0 && nodeLimit <= 0 {
		return groups, nil
	}

	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	result := make(map[string][]showDependencyItem)
	omitted := 0
	consumed := 0

	for _, key := range keys {
		items := groups[key]
		limitedItems := items

		if relationLimit > 0 && len(limitedItems) > relationLimit {
			omitted += len(limitedItems) - relationLimit
			limitedItems = limitedItems[:relationLimit]
		}

		if nodeLimit > 0 {
			if consumed >= nodeLimit {
				omitted += len(limitedItems)
				limitedItems = nil
			} else if consumed+len(limitedItems) > nodeLimit {
				allowed := nodeLimit - consumed
				omitted += len(limitedItems) - allowed
				limitedItems = limitedItems[:allowed]
			}
		}

		if len(limitedItems) > 0 {
			result[key] = limitedItems
			consumed += len(limitedItems)
		}
	}

	if omitted == 0 {
		return result, nil
	}

	return result, &showDependencyTruncation{
		Applied:       true,
		RelationLimit: relationLimit,
		NodeLimit:     nodeLimit,
		Omitted:       omitted,
	}
}

func canonicalShowDependencyType(depType domain.DependencyType) string {
	value := strings.ToLower(strings.ReplaceAll(string(depType), "_", "-"))
	switch value {
	case "blocks":
		return "blocking"
	case "blocked-by", "depends-on":
		return "blocked-by"
	case "parent-child":
		return "parent-child"
	case "discovered-from":
		return "discovered-from"
	case "related", "related-to":
		return "related"
	default:
		return value
	}
}

func showPriorityString(priority domain.Priority) string {
	priorityValue := int(priority)
	if priorityValue < int(domain.P0) || priorityValue > int(domain.P4) {
		return fmt.Sprintf("P%d", priorityValue)
	}
	return priority.String()
}

func handleShowInvalidUsage(
	parseErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			showInvalidArgumentCode,
			parseErr.Error(),
			showInvalidArgRemediation,
			nil,
		)
		envelope := NewH2FailureEnvelope(showCommandName, showCommandPath, project, meta, errorPayload)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return showExitCodeInvalidUsage
	}

	fmt.Fprintln(stderr, showUsage)
	return showExitCodeInvalidUsage
}

func handleShowInternalError(
	commandErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			showInternalErrorCode,
			commandErr.Error(),
			showInternalRemediation,
			nil,
		)
		envelope := NewH2FailureEnvelope(showCommandName, showCommandPath, project, meta, errorPayload)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return 1
	}

	fmt.Fprintf(stderr, "Error: %v\n", commandErr)
	return 1
}

func showProjectContext() H2ProjectContext {
	cwd, err := os.Getwd()
	if err != nil {
		return H2ProjectContext{}
	}

	projectPath := cwd
	projectID := filepath.Base(cwd)

	if registry, regErr := config.LoadProjectsRegistry(); regErr == nil && registry != nil {
		if project := registry.FindByPath(cwd); project != nil {
			projectPath = project.Path
			if strings.TrimSpace(project.Name) != "" {
				projectID = project.Name
			} else {
				projectID = filepath.Base(project.Path)
			}
		}
	}

	return H2ProjectContext{
		ID:              projectID,
		Path:            projectPath,
		CanonicalDBPath: filepath.Join(projectPath, ".azedarach", "azedarach.db"),
	}
}

func showExecutionMeta(startedAt time.Time) H2Meta {
	return H2Meta{
		DurationMs: time.Since(startedAt).Milliseconds(),
		At:         time.Now().UTC().Format(time.RFC3339),
	}
}
