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
)

type BoardViewOptions struct {
	Command string
	Project string
	ViewID  string
	IssueID string
	File    string
	Select  bool
	Confirm bool
	JSON    bool
}

func ParseBoardViewArgs(command string, args []string) (BoardViewOptions, error) {
	opts := BoardViewOptions{Command: command}
	fs := flag.NewFlagSet("board view "+command, flag.ContinueOnError)
	fs.StringVar(&opts.Project, "project", "", "project id")
	fs.BoolVar(&opts.JSON, "json", false, "print JSON")
	switch command {
	case "list":
	case "get":
	case "select":
	case "explain":
		fs.StringVar(&opts.ViewID, "view", "", "board view id")
	case "create", "update":
		fs.StringVar(&opts.File, "file", "", "board view JSON file, or '-' for stdin")
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
	if project := strings.TrimSpace(opts.Project); project != "" {
		client = client.ScopedProjectID(project)
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
		return printBoardViewRecord(resp.View, opts.JSON)
	case "select":
		resp, err := client.SelectBoardView(ctx, opts.ViewID)
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
		resp, err := client.SaveBoardView(ctx, view)
		if err != nil {
			return err
		}
		var selected *protocol.BoardViewSelectResponseBody
		if opts.Select {
			selectResp, err := client.SelectBoardView(ctx, string(resp.View.View.ID))
			if err != nil {
				return err
			}
			selected = &selectResp
		}
		if opts.JSON {
			if selected != nil {
				return printJSON(map[string]any{"view": resp.View, "selected": selected})
			}
			return printJSON(resp.View)
		}
		fmt.Printf("Saved board view %s (%s)\n", resp.View.View.ID, resp.View.View.Title)
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
	for _, record := range resp.Views {
		marker := " "
		if string(record.View.ID) == resp.SelectedViewID {
			marker = "*"
		}
		builtIn := ""
		if record.BuiltIn {
			builtIn = " built-in"
		}
		fmt.Printf("%s %s\t%s\t%d columns%s\n", marker, record.View.ID, record.View.Title, len(record.View.Columns), builtIn)
	}
	fmt.Println()
	fmt.Println("A board view is a saved column projection over issue lifecycle, review, close, and runtime facts; it does not change issue lifecycle status.")
	return nil
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
