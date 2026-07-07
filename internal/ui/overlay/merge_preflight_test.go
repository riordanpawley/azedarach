package overlay

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestMergePreflightOverlayRefreshSelection(t *testing.T) {
	o := NewMergePreflightOverlay("az-1", "main", "/tmp/az-1", ".", []string{"Target main is not clean"}, []string{"foo.go"}, []string{"bar.go"}, nil, "main", "az/az-1", false, true)
	_, cmd := o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd == nil {
		t.Fatal("expected refresh command")
	}
	msg := cmd()
	selection, ok := msg.(SelectionMsg)
	if !ok {
		t.Fatalf("msg type = %T, want SelectionMsg", msg)
	}
	if selection.Key != "merge_preflight_refresh" {
		t.Fatalf("selection key = %q, want merge_preflight_refresh", selection.Key)
	}
}

func TestMergePreflightOverlayAbortSelection(t *testing.T) {
	o := NewMergePreflightOverlay("az-1", "main", "/tmp/az-1", ".", []string{"Target main is not clean"}, nil, nil, nil, "", "", false, true)
	_, cmd := o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if cmd == nil {
		t.Fatal("expected abort command")
	}
	msg := cmd()
	selection, ok := msg.(SelectionMsg)
	if !ok {
		t.Fatalf("msg type = %T, want SelectionMsg", msg)
	}
	if selection.Key != "merge_preflight_abort" {
		t.Fatalf("selection key = %q, want merge_preflight_abort", selection.Key)
	}
	value, ok := selection.Value.(MergePreflightWorktreeSelection)
	if !ok {
		t.Fatalf("selection value = %T, want MergePreflightWorktreeSelection", selection.Value)
	}
	if value.Worktree != "." {
		t.Fatalf("selection worktree = %q, want \".\"", value.Worktree)
	}
}

func TestMergePreflightOverlayIgnoreSourceDirtySelection(t *testing.T) {
	o := NewMergePreflightOverlay("az-1", "base", "/tmp/az-1", ".", []string{"Source az-1 is not clean"}, []string{"foo.go"}, nil, nil, "main", "az/az-1", true, true)
	_, cmd := o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	if cmd == nil {
		t.Fatal("expected ignore source dirty command")
	}
	msg := cmd()
	selection, ok := msg.(SelectionMsg)
	if !ok {
		t.Fatalf("msg type = %T, want SelectionMsg", msg)
	}
	if selection.Key != "merge_preflight_ignore_source_dirty" {
		t.Fatalf("selection key = %q, want merge_preflight_ignore_source_dirty", selection.Key)
	}
	value, ok := selection.Value.(MergePreflightRefreshSelection)
	if !ok {
		t.Fatalf("selection value = %T, want MergePreflightRefreshSelection", selection.Value)
	}
	if !value.IgnoreSourceDirty {
		t.Fatal("ignore source dirty = false, want true")
	}
	if !value.StopTargetBeforeMerge {
		t.Fatal("stop target before merge = false, want true")
	}
	var sawIgnore bool
	for _, binding := range o.actionBindings() {
		if strings.Contains(strings.ToLower(binding.Description), "ignore source dirty") {
			sawIgnore = true
			break
		}
	}
	if !sawIgnore {
		t.Fatal("missing ignore source dirty binding")
	}
}

func TestMergePreflightOverlayDoesNotOfferIgnoreSourceDirtyWhenTargetDirty(t *testing.T) {
	o := NewMergePreflightOverlay("az-1", "base", "/tmp/az-1", ".", []string{"Source and target are not clean"}, []string{"foo.go"}, []string{"bar.go"}, nil, "main", "az/az-1", true, true)
	_, cmd := o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	if cmd != nil {
		t.Fatal("unexpected ignore source dirty command when target is dirty")
	}
	for _, binding := range o.actionBindings() {
		if strings.Contains(strings.ToLower(binding.Description), "ignore") {
			t.Fatalf("unexpected ignore action binding: %+v", binding)
		}
	}
}

func TestMergePreflightOverlayClose(t *testing.T) {
	o := NewMergePreflightOverlay("az-1", "main", "/tmp/az-1", ".", nil, nil, nil, nil, "", "", false, false)
	_, cmd := o.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("expected close command")
	}
	if _, ok := cmd().(CloseOverlayMsg); !ok {
		t.Fatalf("msg type = %T, want CloseOverlayMsg", cmd())
	}
}

func TestMergePreflightOverlayViewContainsReasons(t *testing.T) {
	o := NewMergePreflightOverlay("az-1", "main", "/tmp/az-1", ".", []string{"Source az-1 is not clean: 1 modified"}, []string{"source.go"}, []string{"target.go"}, []string{"conflict.go"}, "main", "az/az-1", false, true)
	view := o.View()
	if !strings.Contains(view, "Merge blocked: into main from az-1") {
		t.Fatalf("view missing title: %q", view)
	}
	if !strings.Contains(view, "Source az-1 is not clean") {
		t.Fatalf("view missing reason: %q", view)
	}
	if !strings.Contains(view, "From worktree: /tmp/az-1") || !strings.Contains(view, "Into worktree: .") {
		t.Fatalf("view missing worktree paths: %q", view)
	}
	if !strings.Contains(view, "source.go") || !strings.Contains(view, "target.go") {
		t.Fatalf("view missing dirty file lists: %q", view)
	}
	if !strings.Contains(view, "Predicted conflict files:") || !strings.Contains(view, "conflict.go") {
		t.Fatalf("view missing predicted conflict files: %q", view)
	}
}

func TestMergePreflightOverlayResponsiveSize(t *testing.T) {
	o := NewMergePreflightOverlay("az-1", "main", "/tmp/az-1", ".", []string{"Source az-1 is not clean: 1 modified"}, []string{"source.go"}, []string{"target.go"}, nil, "main", "az/az-1", false, true)
	o.ApplyWindowSize(tea.WindowSizeMsg{Width: 120, Height: 40})
	width, height := o.Size()
	if width > 120 || height > 40 {
		t.Fatalf("default size = %dx%d, want within 120x40", width, height)
	}
	o.ApplyWindowSize(tea.WindowSizeMsg{Width: 62, Height: 18})
	width, height = o.Size()
	if width > 62 || height > 18 {
		t.Fatalf("small size = %dx%d, want within 62x18", width, height)
	}
}

func TestMergePreflightOverlayCommitSourceSelection(t *testing.T) {
	o := NewMergePreflightOverlay("az-1", "main", "/tmp/az-1", ".", nil, []string{"foo.go"}, nil, nil, "", "", false, true)
	_, cmd := o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if cmd == nil {
		t.Fatal("expected commit source command")
	}
	selection, ok := cmd().(SelectionMsg)
	if !ok {
		t.Fatalf("msg type = %T, want SelectionMsg", cmd())
	}
	if selection.Key != "merge_preflight_commit_source" {
		t.Fatalf("selection key = %q, want merge_preflight_commit_source", selection.Key)
	}
}

func TestMergePreflightOverlayDiscardTargetSelection(t *testing.T) {
	o := NewMergePreflightOverlay("az-1", "main", "/tmp/az-1", ".", nil, nil, []string{"bar.go"}, nil, "", "", false, true)
	_, cmd := o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	if cmd == nil {
		t.Fatal("expected discard target command")
	}
	selection, ok := cmd().(SelectionMsg)
	if !ok {
		t.Fatalf("msg type = %T, want SelectionMsg", cmd())
	}
	if selection.Key != "merge_preflight_discard_target" {
		t.Fatalf("selection key = %q, want merge_preflight_discard_target", selection.Key)
	}
}

func TestMergePreflightOverlayAgentSelection(t *testing.T) {
	o := NewMergePreflightOverlay("az-1", "base", "/tmp/az-1", ".", nil, nil, nil, []string{"conflict.go"}, "main", "az/az-1", false, true)
	_, cmd := o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if cmd == nil {
		t.Fatal("expected agent command")
	}
	selection, ok := cmd().(SelectionMsg)
	if !ok {
		t.Fatalf("msg type = %T, want SelectionMsg", cmd())
	}
	if selection.Key != "merge_preflight_agent" {
		t.Fatalf("selection key = %q, want merge_preflight_agent", selection.Key)
	}
	value, ok := selection.Value.(MergePreflightAgentSelection)
	if !ok {
		t.Fatalf("selection value = %T, want MergePreflightAgentSelection", selection.Value)
	}
	if value.SourceID != "az-1" || value.TargetID != "base" || value.TargetRef != "main" || value.SourceBranch != "az/az-1" {
		t.Fatalf("selection value = %+v", value)
	}
	if len(value.ConflictFiles) != 1 || value.ConflictFiles[0] != "conflict.go" {
		t.Fatalf("conflict files = %+v, want conflict.go", value.ConflictFiles)
	}
}

func TestMergePreflightOverlayDoesNotOfferAgentMergeWhenDirty(t *testing.T) {
	tests := []struct {
		name        string
		sourceFiles []string
		targetFiles []string
	}{
		{name: "source dirty", sourceFiles: []string{"source.go"}},
		{name: "target dirty", targetFiles: []string{"target.go"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := NewMergePreflightOverlay("az-1", "base", "/tmp/az-1", ".", nil, tt.sourceFiles, tt.targetFiles, []string{"conflict.go"}, "main", "az/az-1", false, true)
			_, cmd := o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
			if cmd != nil {
				t.Fatal("unexpected agent merge command for dirty preflight")
			}
			for _, binding := range o.actionBindings() {
				if strings.Contains(strings.ToLower(binding.Description), "agent merge") {
					t.Fatalf("unexpected agent merge action binding: %+v", binding)
				}
			}
		})
	}
}

func TestMergePreflightOverlayPropagatesProjectContext(t *testing.T) {
	context := ProjectActionContext{
		ProjectID:    "project-a",
		ProjectName:  "Project A",
		ProjectPath:  "/repo-a",
		DaemonSocket: "/tmp/az.sock",
		BaseBranch:   "main",
	}
	o := NewMergePreflightOverlay(
		"az-1",
		"base",
		"/tmp/az-1",
		".",
		nil,
		nil,
		nil,
		[]string{"conflict.go"},
		"main",
		"az/az-1",
		false,
		true,
		WithMergePreflightProjectContext(context),
	)

	_, refreshCmd := o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	refreshMsg := refreshCmd().(SelectionMsg)
	refresh, ok := refreshMsg.Value.(MergePreflightRefreshSelection)
	if !ok {
		t.Fatalf("refresh value = %T, want MergePreflightRefreshSelection", refreshMsg.Value)
	}
	if refresh.Context != context {
		t.Fatalf("refresh context = %+v, want %+v", refresh.Context, context)
	}

	_, agentCmd := o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	agentMsg := agentCmd().(SelectionMsg)
	agent, ok := agentMsg.Value.(MergePreflightAgentSelection)
	if !ok {
		t.Fatalf("agent value = %T, want MergePreflightAgentSelection", agentMsg.Value)
	}
	if agent.Context != context {
		t.Fatalf("agent context = %+v, want %+v", agent.Context, context)
	}
}
