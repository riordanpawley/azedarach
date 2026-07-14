package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

type BoardViewOptions struct {
	Command       string
	Project       string
	Consumer      string
	Scope         string
	ScopeProjects string
	ViewID        string
	IssueID       string
	File          string
	Select        bool
	Confirm       bool
	JSON          bool
}

func ParseBoardViewArgs(command string, args []string) (BoardViewOptions, error) {
	opts := BoardViewOptions{Command: command}
	fs := flag.NewFlagSet("board view "+command, flag.ContinueOnError)
	fs.StringVar(&opts.Project, "project", "", "project id")
	fs.BoolVar(&opts.JSON, "json", false, "print JSON")
	switch command {
	case "list":
	case "get":
		fs.StringVar(&opts.ViewID, "view", "", "board view id")
	case "select":
		fs.StringVar(&opts.Consumer, "consumer", "global_board", "global view consumer (global_board, tmux_selector, search, review)")
		fs.StringVar(&opts.ViewID, "view", "", "board view id")
	case "explain":
		fs.StringVar(&opts.ViewID, "view", "", "board view id")
	case "create", "update":
		fs.StringVar(&opts.File, "file", "", "board view JSON file, or '-' for stdin")
		fs.StringVar(&opts.Scope, "scope", "", "global view scope (all_projects, selected_projects, current_project); update preserves scope when omitted")
		fs.StringVar(&opts.ScopeProjects, "scope-projects", "", "comma-separated canonical project ids for selected/current scope")
		fs.BoolVar(&opts.Select, "select", false, "select the view after saving it")
	case "delete":
		fs.BoolVar(&opts.Confirm, "confirm", false, "confirm deletion")
	default:
		return BoardViewOptions{}, fmt.Errorf("unknown board view command: %s", command)
	}
	if err := fs.Parse(args); err != nil {
		return BoardViewOptions{}, err
	}
	rest := fs.Args()
	switch command {
	case "get", "select", "delete":
		if len(rest) > 1 {
			return BoardViewOptions{}, fmt.Errorf("unexpected arguments: %s", strings.Join(rest[1:], " "))
		}
		if opts.ViewID == "" && len(rest) == 1 {
			opts.ViewID = rest[0]
		}
		if strings.TrimSpace(opts.ViewID) == "" {
			return BoardViewOptions{}, fmt.Errorf("view id is required")
		}
	case "explain":
		if len(rest) != 1 {
			return BoardViewOptions{}, fmt.Errorf("issue id is required")
		}
		opts.ViewID = strings.TrimSpace(opts.ViewID)
		opts.IssueID = rest[0]
	case "create", "update":
		if len(rest) > 0 {
			return BoardViewOptions{}, fmt.Errorf("unexpected arguments: %s", strings.Join(rest, " "))
		}
		if strings.TrimSpace(opts.File) == "" {
			return BoardViewOptions{}, fmt.Errorf("--file is required")
		}
	case "list":
		if len(rest) > 0 {
			return BoardViewOptions{}, fmt.Errorf("unexpected arguments: %s", strings.Join(rest, " "))
		}
	}
	return opts, nil
}

func BoardViewCommand(deps *Dependencies, opts BoardViewOptions) error {
	if deps == nil || deps.DaemonClient == nil {
		return fmt.Errorf("daemon client unavailable")
	}
	client := deps.DaemonClient
	project := strings.TrimSpace(opts.Project)
	globalProject := protocol.NormalizeProjectID(project) == "global"
	if project != "" && !globalProject {
		restoreProject, err := applyExplicitProjectOverride(deps, project)
		if err != nil {
			return err
		}
		defer restoreProject()
		client = deps.DaemonClient
	} else if globalProject {
		client = client.ScopedProjectID("global")
	}
	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()

	switch opts.Command {
	case "list":
		resp, err := client.ListBoardViews(ctx)
		if err != nil {
			return err
		}
		return printBoardViewList(resp, opts.JSON)
	case "get":
		resp, err := client.GetBoardView(ctx, opts.ViewID)
		if err != nil {
			return err
		}
		if resp.GlobalView != nil {
			if opts.JSON {
				return printJSON(map[string]any{"global_view": resp.GlobalView, "selections": resp.Selections})
			}
			if err := printGlobalViewRecord(*resp.GlobalView, false); err != nil {
				return err
			}
			printGlobalViewSelections(resp.Selections)
			return nil
		}
		return printBoardViewRecord(resp.View, opts.JSON)
	case "select":
		var resp protocol.BoardViewSelectResponseBody
		var err error
		if globalProject {
			consumer := protocol.GlobalViewConsumer(strings.TrimSpace(opts.Consumer))
			if !consumer.Valid() {
				return fmt.Errorf("invalid global view consumer %q (want global_board, tmux_selector, search, or review)", opts.Consumer)
			}
			resp, err = client.SelectGlobalView(ctx, consumer, opts.ViewID)
		} else {
			resp, err = client.SelectBoardView(ctx, opts.ViewID)
		}
		if err != nil {
			return err
		}
		if opts.JSON {
			return printJSON(resp)
		}
		fmt.Printf("Selected board view %s for project %s\n", resp.ViewID, resp.ProjectID)
		return nil
	case "create", "update":
		view, err := loadBoardViewDefinition(opts.File)
		if err != nil {
			return err
		}
		var resp protocol.BoardViewResponseBody
		if globalProject {
			var scope protocol.GlobalViewScope
			var scopeErr error
			if strings.TrimSpace(opts.Scope) != "" || strings.TrimSpace(opts.ScopeProjects) != "" {
				scope, scopeErr = parseGlobalViewScope(opts.Scope, opts.ScopeProjects)
			}
			if scopeErr != nil {
				return scopeErr
			}
			resp, err = client.SaveGlobalView(ctx, protocol.GlobalViewRecord{View: view, Scope: scope})
		} else {
			resp, err = client.SaveBoardView(ctx, view)
		}
		if err != nil {
			return err
		}
		var selected *protocol.BoardViewSelectResponseBody
		if opts.Select {
			var selectResp protocol.BoardViewSelectResponseBody
			if globalProject {
				consumer := protocol.GlobalViewConsumer(strings.TrimSpace(opts.Consumer))
				if consumer == "" {
					consumer = protocol.GlobalViewConsumerBoard
				}
				selectResp, err = client.SelectGlobalView(ctx, consumer, string(resp.View.View.ID))
			} else {
				selectResp, err = client.SelectBoardView(ctx, string(resp.View.View.ID))
			}
			if err != nil {
				return err
			}
			selected = &selectResp
		}
		if opts.JSON {
			if selected != nil {
				return printJSON(map[string]any{"view": resp.View, "global_view": resp.GlobalView, "selected": selected})
			}
			if resp.GlobalView != nil {
				return printJSON(resp.GlobalView)
			}
			return printJSON(resp.View)
		}
		fmt.Printf("Saved board view %s (%s)\n", resp.View.View.ID, resp.View.View.Title)
		if resp.GlobalView != nil {
			fmt.Printf("Scope: %s\n", formatGlobalViewScope(resp.GlobalView.Scope))
		}
		if selected != nil {
			fmt.Printf("Selected board view %s for project %s\n", selected.ViewID, selected.ProjectID)
		}
		return nil
	case "delete":
		if !opts.Confirm {
			return fmt.Errorf("delete requires --confirm")
		}
		if err := client.DeleteBoardView(ctx, opts.ViewID); err != nil {
			return err
		}
		if opts.JSON {
			return printJSON(map[string]any{"deleted": true, "view_id": opts.ViewID})
		}
		fmt.Printf("Deleted board view %s\n", opts.ViewID)
		return nil
	case "explain":
		return explainBoardViewPlacement(ctx, client, opts)
	default:
		return fmt.Errorf("unknown board view command: %s", opts.Command)
	}
}

func parseGlobalViewScope(kind, rawProjects string) (protocol.GlobalViewScope, error) {
	if strings.TrimSpace(kind) == "" && strings.TrimSpace(rawProjects) != "" {
		return protocol.GlobalViewScope{}, fmt.Errorf("--scope is required with --scope-projects")
	}
	scope := protocol.GlobalViewScope{Kind: protocol.GlobalViewScopeKind(strings.TrimSpace(kind))}
	for _, raw := range strings.Split(rawProjects, ",") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if id := protocol.NormalizeProjectID(raw); id != "" {
			scope.ProjectIDs = append(scope.ProjectIDs, naming.ProjectID(id))
		}
	}
	if scope.Kind == protocol.GlobalViewScopeCurrentProject && len(scope.ProjectIDs) == 1 {
		scope.CurrentProjectID = scope.ProjectIDs[0]
		scope.ProjectIDs = nil
	}
	if err := scope.Validate(); err != nil {
		return protocol.GlobalViewScope{}, err
	}
	return scope, nil
}

func loadBoardViewDefinition(path string) (domain.BoardView, error) {
	var data []byte
	var err error
	if strings.TrimSpace(path) == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return domain.BoardView{}, fmt.Errorf("read board view definition: %w", err)
	}
	if view, err := domain.DecodeBoardViewDefinitionJSON(data); err == nil {
		return view, nil
	} else if hasBoardViewSchemaVersion(data) {
		return domain.BoardView{}, fmt.Errorf("decode board view definition: %w", err)
	}
	var view domain.BoardView
	if err := json.Unmarshal(data, &view); err != nil {
		return domain.BoardView{}, fmt.Errorf("decode board view definition: %w", err)
	}
	view = view.Normalized()
	if err := view.Validate(); err != nil {
		return domain.BoardView{}, err
	}
	return view, nil
}

func hasBoardViewSchemaVersion(data []byte) bool {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	_, ok := raw["schema_version"]
	return ok
}

func printBoardViewList(resp protocol.BoardViewListResponseBody, asJSON bool) error {
	if asJSON {
		return printJSON(resp)
	}
	fmt.Printf("Board views for project %s:\n", resp.ProjectID)
	scopes := make(map[domain.BoardViewID]protocol.GlobalViewScope, len(resp.GlobalViews))
	for _, record := range resp.GlobalViews {
		scopes[record.View.ID] = record.Scope
	}
	for _, record := range resp.Views {
		marker := " "
		if string(record.View.ID) == resp.SelectedViewID {
			marker = "*"
		}
		builtIn := ""
		if record.BuiltIn {
			builtIn = " built-in"
		}
		scope := ""
		if value, ok := scopes[record.View.ID]; ok {
			scope = " scope=" + formatGlobalViewScope(value)
		}
		fmt.Printf("%s %s\t%s\t%d columns%s%s\n", marker, record.View.ID, record.View.Title, len(record.View.Columns), builtIn, scope)
	}
	printGlobalViewSelections(resp.Selections)
	fmt.Println()
	fmt.Println("A board view is a saved column projection over issue lifecycle, review, close, and runtime facts; it does not change issue lifecycle status.")
	fmt.Println("Built-ins: default (delivery), planning (intake), orchestration (waiting/activity), closeout (review and closed outcomes). Legacy current/activity IDs alias to default/orchestration.")
	return nil
}

func printGlobalViewSelections(selections map[protocol.GlobalViewConsumer]string) {
	if len(selections) == 0 {
		return
	}
	fmt.Println("Consumer selections:")
	for _, consumer := range []protocol.GlobalViewConsumer{protocol.GlobalViewConsumerBoard, protocol.GlobalViewConsumerTmuxSelector, protocol.GlobalViewConsumerSearch, protocol.GlobalViewConsumerReview} {
		fmt.Printf("- %s: %s\n", consumer, selections[consumer])
	}
}

func printGlobalViewRecord(record protocol.GlobalViewRecord, asJSON bool) error {
	if asJSON {
		return printJSON(record)
	}
	fmt.Printf("%s - %s\n", record.View.ID, record.View.Title)
	fmt.Printf("Project: global\nScope: %s\n", formatGlobalViewScope(record.Scope))
	fmt.Println("Columns:")
	for _, column := range record.View.Columns {
		fmt.Printf("- %s (%s): %s\n", column.Title, column.ID, formatBoardColumnPredicates(column.Predicates))
	}
	return nil
}

func formatGlobalViewScope(scope protocol.GlobalViewScope) string {
	switch scope.Kind {
	case protocol.GlobalViewScopeSelectedProjects:
		ids := make([]string, 0, len(scope.ProjectIDs))
		for _, id := range scope.ProjectIDs {
			ids = append(ids, id.String())
		}
		return string(scope.Kind) + " (" + strings.Join(ids, ",") + ")"
	case protocol.GlobalViewScopeCurrentProject:
		return string(scope.Kind) + " (" + scope.CurrentProjectID.String() + ")"
	case "":
		return string(protocol.GlobalViewScopeAllProjects)
	default:
		return string(scope.Kind)
	}
}

func printBoardViewRecord(record domain.BoardViewRecord, asJSON bool) error {
	if asJSON {
		return printJSON(record)
	}
	fmt.Printf("%s - %s\n", record.View.ID, record.View.Title)
	fmt.Printf("Project: %s\n", record.ProjectID)
	fmt.Printf("Built-in: %t\n", record.BuiltIn)
	fmt.Println("Columns:")
	for _, column := range record.View.Columns {
		fmt.Printf("- %s (%s): %s\n", column.Title, column.ID, formatBoardColumnPredicates(column.Predicates))
	}
	fmt.Println()
	fmt.Println("Column membership is derived from issue facts at read time; status/lifecycle changes remain separate durable actions.")
	return nil
}

func explainBoardViewPlacement(ctx context.Context, client *daemonclient.Client, opts BoardViewOptions) error {
	viewResp, err := client.GetBoardView(ctx, opts.ViewID)
	if err != nil {
		return err
	}
	snapshot, err := client.GetTaskSnapshotWithArchiveMode(ctx, opts.IssueID, protocol.ArchiveModeInclude, daemonclient.ReadWaitModeExplicit)
	if err != nil {
		return err
	}
	if len(snapshot.Tasks) == 0 {
		return fmt.Errorf("issue %s not found", opts.IssueID)
	}
	task := snapshot.Tasks[0]
	placement, err := viewResp.View.View.PlaceTask(task)
	if err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(map[string]any{
			"issue_id":  task.ID.String(),
			"view_id":   viewResp.View.View.ID,
			"placement": placement,
		})
	}
	fmt.Printf("%s in view %s (%s):\n", task.ID, viewResp.View.View.ID, viewResp.View.View.Title)
	if !placement.Matched {
		fmt.Printf("No column matched: %s\n", placement.MatchReason)
		return nil
	}
	fmt.Printf("Column: %s (%s)\n", placement.ColumnTitle, placement.ColumnID)
	fmt.Printf("Reason: %s\n", placement.MatchReason)
	return nil
}

func formatBoardColumnPredicates(predicates []domain.BoardColumnPredicate) string {
	if len(predicates) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(predicates))
	for _, predicate := range predicates {
		switch predicate.Kind {
		case domain.BoardPredicateLifecycle:
			parts = append(parts, fmt.Sprintf("lifecycle in [%s]", joinStringers(predicate.Lifecycle)))
		case domain.BoardPredicateDisplayPhase:
			parts = append(parts, fmt.Sprintf("display phase in [%s]", joinStringers(predicate.DisplayPhases)))
		case domain.BoardPredicateClosedOutcome:
			parts = append(parts, fmt.Sprintf("closed outcome in [%s]", joinStringers(predicate.ClosedOutcomes)))
		default:
			parts = append(parts, string(predicate.Kind))
		}
	}
	return strings.Join(parts, " + ")
}

func joinStringers[T ~string](values []T) string {
	if len(values) == 0 {
		return ""
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, string(value))
	}
	return strings.Join(out, ",")
}
