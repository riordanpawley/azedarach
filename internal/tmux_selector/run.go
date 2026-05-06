package tmuxselector

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

func Run(cfg *config.Config) error {
	_ = cfg
	logger := slog.Default()
	tmuxClient := tmux.NewClient(&tmux.ExecRunner{}, logger)
	loader := NewDefaultGlobalInventoryLoader(tmuxClient, logger)
	if !hasControllingTTY() {
		return RunPlain(context.Background(), loader, os.Stdout)
	}
	model := New(
		loader,
		WithSwitcher(tmuxClient),
		WithDetailOpener(NewDaemonDetailOpener(logger)),
	)
	p := tea.NewProgram(model, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func RunPlain(ctx context.Context, loader SnapshotLoader, out io.Writer) error {
	if loader == nil {
		return fmt.Errorf("snapshot loader unavailable")
	}
	if out == nil {
		out = io.Discard
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	snapshot, err := loader.ListTasksSnapshot(ctx)
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(out, PlainSnapshot(snapshot))
	return err
}

func PlainSnapshot(snapshot Snapshot) string {
	entries := snapshot.Entries
	if len(entries) == 0 && len(snapshot.Tasks) > 0 {
		entries = EntriesFromTasks(snapshot.Tasks)
	}
	if len(entries) == 0 {
		return "No tmux sessions found.\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d %s\n", len(entries), pluralize(len(entries), "tmux session", "tmux sessions"))
	for _, entry := range entries {
		sessionID := strings.TrimSpace(entry.SessionID)
		if sessionID == "" {
			sessionID = strings.TrimSpace(entry.IssueID)
		}
		if sessionID == "" {
			continue
		}
		title := strings.TrimSpace(entry.TaskTitle)
		if title == "" {
			title = strings.TrimSpace(entry.Task.Title)
		}
		if title == "" {
			title = sessionID
		}
		fmt.Fprintf(&b, "- %s", sessionID)
		if issueID := strings.TrimSpace(entry.IssueID); issueID != "" && issueID != sessionID {
			fmt.Fprintf(&b, " (%s)", issueID)
		}
		fmt.Fprintf(&b, ": %s", title)
		if project := strings.TrimSpace(firstNonEmpty(entry.ProjectPath, entry.ProjectID)); project != "" {
			fmt.Fprintf(&b, " [%s]", project)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func pluralize(count int, singular string, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}

func hasControllingTTY() bool {
	file, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = file.Close()
	return true
}
