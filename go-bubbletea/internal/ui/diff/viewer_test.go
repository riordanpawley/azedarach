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
	changedFiles []gitservice.ChangedFile
	changedErr   error
	mergeBase    string
	mergeBaseErr error
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
			{Path: "internal/app/model.go", Status: gitservice.DiffFileModified},
		},
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
	if viewer.files[0].Path != "internal/app/model.go" {
		t.Fatalf("file path=%q, want internal/app/model.go", viewer.files[0].Path)
	}
}

func TestDiffViewerEnterOpensSelectedFilePopup(t *testing.T) {
	client := &fakeDiffClient{
		changedFiles: []gitservice.ChangedFile{
			{Path: "internal/app/model.go", Status: gitservice.DiffFileModified},
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

	if gotTitle != " internal/app/model.go " {
		t.Fatalf("popup title=%q", gotTitle)
	}
	if !strings.Contains(gotCommand, "git diff 'base123' -- 'internal/app/model.go'") {
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
	if !strings.Contains(gotCommand, "git diff 'base456' --stat --color=always -- ':^.azedarach'") {
		t.Fatalf("popup command=%q", gotCommand)
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
