package diff

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	gitservice "github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

type fakeDiffClient struct {
	status      *gitservice.GitStatus
	statusErr   error
	changedFiles []gitservice.ChangedFile
	changedErr   error
	mergeBase    string
	mergeBaseErr error
}

func (f *fakeDiffClient) Status(context.Context, string) (*gitservice.GitStatus, error) {
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	if f.status == nil {
		return &gitservice.GitStatus{}, nil
	}
	copied := *f.status
	return &copied, nil
}

func (f *fakeDiffClient) ChangedFiles(context.Context, string, string) ([]gitservice.ChangedFile, error) {
	if f.changedErr != nil {
		return nil, f.changedErr
	}
	return append([]gitservice.ChangedFile(nil), f.changedFiles...), nil
}

func (f *fakeDiffClient) MergeBase(context.Context, string, string) (string, error) {
	if f.mergeBaseErr != nil {
		return "", f.mergeBaseErr
	}
	if strings.TrimSpace(f.mergeBase) == "" {
		return "abc123", nil
	}
	return f.mergeBase, nil
}

func TestDiffViewerInitLoadsChangedFiles(t *testing.T) {
	client := &fakeDiffClient{
		changedFiles: []gitservice.ChangedFile{
			{Path: "internal/tui/model.go", Status: gitservice.DiffFileModified},
		},
		mergeBase: "base123",
	}

	viewer := NewDiffViewer("/tmp/az-1", "main", client, nil)
	msg := viewer.Init()()
	updated, _ := viewer.Update(msg)
	viewer = updated.(*DiffViewer)

	if viewer.loading {
		t.Fatal("expected loading=false after init message")
	}
	if len(viewer.files) != 1 {
		t.Fatalf("files=%d, want 1", len(viewer.files))
	}
	if viewer.files[0].Path != "internal/tui/model.go" {
		t.Fatalf("file path=%q, want internal/tui/model.go", viewer.files[0].Path)
	}
}

func TestDiffViewerEnterOpensSelectedFilePopup(t *testing.T) {
	client := &fakeDiffClient{
		changedFiles: []gitservice.ChangedFile{
			{Path: "internal/tui/model.go", Status: gitservice.DiffFileModified},
		},
		mergeBase: "base123",
	}

	var gotTitle string
	var gotCommand string
	viewer := NewDiffViewer("/tmp/az-1", "main", client, func(_ context.Context, title, command string) error {
		gotTitle = title
		gotCommand = command
		return nil
	})

	msg := viewer.Init()()
	updated, _ := viewer.Update(msg)
	viewer = updated.(*DiffViewer)

	updated, cmd := viewer.Update(tea.KeyMsg{Type: tea.KeyEnter})
	viewer = updated.(*DiffViewer)
	if cmd == nil {
		t.Fatal("expected popup command")
	}
	result := cmd()
	updated, _ = viewer.Update(result)
	viewer = updated.(*DiffViewer)

	if gotTitle != " internal/tui/model.go " {
		t.Fatalf("popup title=%q", gotTitle)
	}
	if !strings.Contains(gotCommand, "BASE_BRANCH='main'; BASE_REF=\"$BASE_BRANCH\";") {
		t.Fatalf("popup command=%q", gotCommand)
	}
	if !strings.Contains(gotCommand, "git diff \"$BASE_REF\"...HEAD -- 'internal/tui/model.go'") {
		t.Fatalf("popup command=%q", gotCommand)
	}
	if !strings.Contains(viewer.popupStatus, "Opened diff popup") {
		t.Fatalf("popup status=%q", viewer.popupStatus)
	}
}

func TestDiffViewerAllDiffPopup(t *testing.T) {
	client := &fakeDiffClient{
		changedFiles: []gitservice.ChangedFile{
			{Path: "a.go", Status: gitservice.DiffFileModified},
		},
		mergeBase: "base456",
	}

	var gotCommand string
	viewer := NewDiffViewer("/tmp/az-1", "main", client, func(_ context.Context, _ string, command string) error {
		gotCommand = command
		return nil
	})
	msg := viewer.Init()()
	updated, _ := viewer.Update(msg)
	viewer = updated.(*DiffViewer)

	updated, cmd := viewer.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	viewer = updated.(*DiffViewer)
	if cmd == nil {
		t.Fatal("expected all-diff popup command")
	}
	_ = cmd()
	if !strings.Contains(gotCommand, "git diff \"$BASE_REF\"...HEAD --stat --color=always -- ':^.azedarach'") {
		t.Fatalf("popup command=%q", gotCommand)
	}
}

func TestDiffViewerSearchFiltersAndKeepsSelectionActions(t *testing.T) {
	client := &fakeDiffClient{
		changedFiles: []gitservice.ChangedFile{
			{Path: "internal/tui/model.go", Status: gitservice.DiffFileModified},
			{Path: "internal/services/git/client.go", Status: gitservice.DiffFileModified},
		},
	}

	var gotTitle string
	viewer := NewDiffViewer("/tmp/az-1", "main", client, func(_ context.Context, title, _ string) error {
		gotTitle = title
		return nil
	})
	msg := viewer.Init()()
	updated, _ := viewer.Update(msg)
	viewer = updated.(*DiffViewer)

	updated, _ = viewer.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	viewer = updated.(*DiffViewer)
	updated, _ = viewer.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	viewer = updated.(*DiffViewer)
	updated, _ = viewer.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	viewer = updated.(*DiffViewer)
	updated, _ = viewer.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	viewer = updated.(*DiffViewer)

	if !viewer.searchMode {
		t.Fatal("expected viewer in search mode")
	}
	if viewer.filterText != "git" {
		t.Fatalf("filter=%q, want git", viewer.filterText)
	}
	if len(viewer.filteredFiles()) != 1 {
		t.Fatalf("filtered len=%d, want 1", len(viewer.filteredFiles()))
	}

	updated, _ = viewer.Update(tea.KeyMsg{Type: tea.KeyEnter})
	viewer = updated.(*DiffViewer)
	if viewer.searchMode {
		t.Fatal("expected Enter to exit search mode")
	}

	updated, cmd := viewer.Update(tea.KeyMsg{Type: tea.KeyEnter})
	viewer = updated.(*DiffViewer)
	if cmd == nil {
		t.Fatal("expected popup command after search filter")
	}
	_ = cmd()
	if gotTitle != " internal/services/git/client.go " {
		t.Fatalf("title=%q, want filtered file popup title", gotTitle)
	}
}

func TestDiffViewerEscInSearchModeClearsFilter(t *testing.T) {
	client := &fakeDiffClient{
		changedFiles: []gitservice.ChangedFile{
			{Path: "internal/tui/model.go", Status: gitservice.DiffFileModified},
		},
	}
	viewer := NewDiffViewer("/tmp/az-1", "main", client, nil)
	msg := viewer.Init()()
	updated, _ := viewer.Update(msg)
	viewer = updated.(*DiffViewer)

	updated, _ = viewer.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	viewer = updated.(*DiffViewer)
	updated, _ = viewer.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	viewer = updated.(*DiffViewer)
	if !viewer.searchMode || viewer.filterText != "m" {
		t.Fatalf("unexpected search state: search=%v filter=%q", viewer.searchMode, viewer.filterText)
	}

	updated, cmd := viewer.Update(tea.KeyMsg{Type: tea.KeyEsc})
	viewer = updated.(*DiffViewer)
	if cmd != nil {
		t.Fatal("expected esc in search mode to not close overlay")
	}
	if viewer.searchMode {
		t.Fatal("expected esc to exit search mode")
	}
	if viewer.filterText != "" {
		t.Fatalf("filter=%q, want empty", viewer.filterText)
	}
}

func TestDiffViewerSearchClampsCursorAfterFilterEdit(t *testing.T) {
	client := &fakeDiffClient{
		changedFiles: []gitservice.ChangedFile{
			{Path: "alpha.go", Status: gitservice.DiffFileModified},
			{Path: "beta.go", Status: gitservice.DiffFileModified},
			{Path: "gamma.go", Status: gitservice.DiffFileModified},
			{Path: "internal/services/git/client.go", Status: gitservice.DiffFileModified},
		},
	}

	var gotTitle string
	viewer := NewDiffViewer("/tmp/az-1", "main", client, func(_ context.Context, title, _ string) error {
		gotTitle = title
		return nil
	})
	msg := viewer.Init()()
	updated, _ := viewer.Update(msg)
	viewer = updated.(*DiffViewer)

	viewer.cursor = 3

	updated, _ = viewer.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	viewer = updated.(*DiffViewer)
	updated, _ = viewer.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	viewer = updated.(*DiffViewer)
	updated, _ = viewer.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	viewer = updated.(*DiffViewer)
	updated, _ = viewer.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	viewer = updated.(*DiffViewer)

	if viewer.cursor != 0 {
		t.Fatalf("cursor=%d, want 0 after filter narrows to one result", viewer.cursor)
	}

	updated, _ = viewer.Update(tea.KeyMsg{Type: tea.KeyEnter})
	viewer = updated.(*DiffViewer)
	updated, cmd := viewer.Update(tea.KeyMsg{Type: tea.KeyEnter})
	viewer = updated.(*DiffViewer)
	if cmd == nil {
		t.Fatal("expected popup command")
	}
	_ = cmd()
	if gotTitle != " internal/services/git/client.go " {
		t.Fatalf("title=%q, want filtered file popup title", gotTitle)
	}
}

func TestDiffViewerClose(t *testing.T) {
	viewer := NewDiffViewer("/tmp/az-1", "main", &fakeDiffClient{}, nil)
	updated, cmd := viewer.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if _, ok := updated.(*DiffViewer); !ok {
		t.Fatalf("updated model type=%T", updated)
	}
	if cmd == nil {
		t.Fatal("expected close command")
	}
	if _, ok := cmd().(overlay.CloseOverlayMsg); !ok {
		t.Fatalf("message type=%T, want overlay.CloseOverlayMsg", cmd())
	}
}

func TestStatusChangedFilesDeduplicatesAndSortsWithPriority(t *testing.T) {
	status := &gitservice.GitStatus{
		Modified:  []string{"z.go", "a.go"},
		Staged:    []string{"b.go", "a.go"},
		Added:     []string{"c.go"},
		Untracked: []string{"u.go"},
		Deleted:   []string{"z.go"},
	}

	files := statusChangedFiles(status)
	if len(files) != 5 {
		t.Fatalf("files len=%d, want 5", len(files))
	}
	if files[0].Path != "a.go" || files[0].Status != gitservice.DiffFileModified {
		t.Fatalf("files[0]=%+v", files[0])
	}
	if files[1].Path != "b.go" || files[1].Status != gitservice.DiffFileModified {
		t.Fatalf("files[1]=%+v", files[1])
	}
	if files[2].Path != "c.go" || files[2].Status != gitservice.DiffFileAdded {
		t.Fatalf("files[2]=%+v", files[2])
	}
	if files[3].Path != "u.go" || files[3].Status != gitservice.DiffFileAdded {
		t.Fatalf("files[3]=%+v", files[3])
	}
	if files[4].Path != "z.go" || files[4].Status != gitservice.DiffFileDeleted {
		t.Fatalf("files[4]=%+v", files[4])
	}
}

func TestDiffViewerInitFallsBackToBaseChangedFilesWhenStatusClean(t *testing.T) {
	client := &fakeDiffClient{
		status: &gitservice.GitStatus{},
		changedFiles: []gitservice.ChangedFile{
			{Path: "internal/tui/model.go", Status: gitservice.DiffFileModified},
		},
	}

	viewer := NewDiffViewer("/tmp/az-1", "main", client, nil)
	msg := viewer.Init()()
	updated, _ := viewer.Update(msg)
	viewer = updated.(*DiffViewer)

	if len(viewer.files) != 1 {
		t.Fatalf("files=%d, want 1", len(viewer.files))
	}
	if viewer.files[0].Path != "internal/tui/model.go" {
		t.Fatalf("file path=%q, want internal/tui/model.go", viewer.files[0].Path)
	}
}
