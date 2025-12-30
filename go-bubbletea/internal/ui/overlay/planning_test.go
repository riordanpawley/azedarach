package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPlanningOverlay(t *testing.T) {
	overlay := NewPlanningOverlay()

	require.NotNil(t, overlay)
	assert.Equal(t, phaseInput, overlay.phase)
	assert.True(t, overlay.focusInput)
	assert.Equal(t, domain.PlanningIdle, overlay.state.Status)
}

func TestPlanningOverlay_UpdateState(t *testing.T) {
	tests := []struct {
		name      string
		status    domain.PlanningStatus
		wantPhase planningPhase
	}{
		{
			name:      "idle status",
			status:    domain.PlanningIdle,
			wantPhase: phaseInput,
		},
		{
			name:      "generating status",
			status:    domain.PlanningGenerating,
			wantPhase: phaseProgress,
		},
		{
			name:      "reviewing status",
			status:    domain.PlanningReviewing,
			wantPhase: phaseProgress,
		},
		{
			name:      "refining status",
			status:    domain.PlanningRefining,
			wantPhase: phaseProgress,
		},
		{
			name:      "creating beads status",
			status:    domain.PlanningCreatingBeads,
			wantPhase: phaseProgress,
		},
		{
			name:      "complete status",
			status:    domain.PlanningComplete,
			wantPhase: phaseComplete,
		},
		{
			name:      "error status",
			status:    domain.PlanningErrorStatus,
			wantPhase: phaseError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			overlay := NewPlanningOverlay()
			state := domain.PlanningState{Status: tt.status}

			overlay.UpdateState(state)

			assert.Equal(t, tt.wantPhase, overlay.phase)
			assert.Equal(t, tt.status, overlay.state.Status)
		})
	}
}

func TestPlanningOverlay_InputPhase(t *testing.T) {
	tests := []struct {
		name        string
		key         string
		inputValue  string
		expectMsg   bool
		expectClose bool
	}{
		{
			name:        "escape closes overlay",
			key:         "esc",
			expectClose: true,
		},
		{
			name:       "tab switches focus",
			key:        "tab",
			expectMsg:  false,
		},
		{
			name:       "enter with empty input does nothing",
			key:        "enter",
			inputValue: "",
			expectMsg:  false,
		},
		{
			name:       "enter with description starts planning",
			key:        "enter",
			inputValue: "Add user authentication",
			expectMsg:  true,
		},
		{
			name:       "ctrl+s with description starts planning",
			key:        "ctrl+s",
			inputValue: "Add user authentication",
			expectMsg:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			overlay := NewPlanningOverlay()
			overlay.input.SetValue(tt.inputValue)

			msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tt.key), Alt: false}
			if tt.key == "esc" {
				msg = tea.KeyMsg{Type: tea.KeyEsc}
			} else if tt.key == "enter" {
				msg = tea.KeyMsg{Type: tea.KeyEnter}
			} else if tt.key == "tab" {
				msg = tea.KeyMsg{Type: tea.KeyTab}
			}

			model, cmd := overlay.Update(msg)
			updatedOverlay := model.(*PlanningOverlay)

			if tt.expectClose {
				require.NotNil(t, cmd)
				closeMsg := cmd()
				_, ok := closeMsg.(CloseOverlayMsg)
				assert.True(t, ok, "expected CloseOverlayMsg")
			} else if tt.expectMsg {
				require.NotNil(t, cmd)
				planMsg := cmd()
				startMsg, ok := planMsg.(PlanningStartMsg)
				assert.True(t, ok, "expected PlanningStartMsg")
				assert.NotEmpty(t, startMsg.Description)
			}

			if tt.key == "tab" {
				assert.False(t, updatedOverlay.focusInput, "focus should toggle")
			}
		})
	}
}

func TestPlanningOverlay_ProgressPhase(t *testing.T) {
	overlay := NewPlanningOverlay()
	overlay.phase = phaseProgress
	overlay.state.Status = domain.PlanningGenerating

	// Test escape closes overlay
	msg := tea.KeyMsg{Type: tea.KeyEsc}
	_, cmd := overlay.Update(msg)

	require.NotNil(t, cmd)
	closeMsg := cmd()
	_, ok := closeMsg.(CloseOverlayMsg)
	assert.True(t, ok, "expected CloseOverlayMsg")
}

func TestPlanningOverlay_CompletePhase(t *testing.T) {
	tests := []struct {
		name        string
		key         tea.KeyType
		expectClose bool
		expectReset bool
	}{
		{
			name:        "escape closes",
			key:         tea.KeyEsc,
			expectClose: true,
		},
		{
			name:        "enter closes",
			key:         tea.KeyEnter,
			expectClose: true,
		},
		{
			name:        "r resets",
			key:         tea.KeyRunes,
			expectReset: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			overlay := NewPlanningOverlay()
			overlay.phase = phaseComplete
			overlay.state.Status = domain.PlanningComplete
			overlay.state.CreatedBeads = []domain.Task{
				{ID: "az-1", Title: "Epic"},
				{ID: "az-2", Title: "Task 1"},
			}

			var msg tea.KeyMsg
			if tt.key == tea.KeyRunes {
				msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")}
			} else {
				msg = tea.KeyMsg{Type: tt.key}
			}

			model, cmd := overlay.Update(msg)
			updatedOverlay := model.(*PlanningOverlay)

			if tt.expectClose {
				require.NotNil(t, cmd)
			}

			if tt.expectReset {
				assert.Equal(t, phaseInput, updatedOverlay.phase)
				assert.Empty(t, updatedOverlay.input.Value())
			}
		})
	}
}

func TestPlanningOverlay_ErrorPhase(t *testing.T) {
	overlay := NewPlanningOverlay()
	overlay.phase = phaseError
	overlay.state.Status = domain.PlanningErrorStatus
	overlay.state.Error = "API error"

	// Test retry goes back to input
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")}
	model, _ := overlay.Update(msg)
	updatedOverlay := model.(*PlanningOverlay)

	assert.Equal(t, phaseInput, updatedOverlay.phase)

	// Test escape closes
	overlay.phase = phaseError
	msg = tea.KeyMsg{Type: tea.KeyEsc}
	_, cmd := overlay.Update(msg)

	require.NotNil(t, cmd)
	closeMsg := cmd()
	_, ok := closeMsg.(CloseOverlayMsg)
	assert.True(t, ok, "expected CloseOverlayMsg")
}

func TestPlanningOverlay_View(t *testing.T) {
	tests := []struct {
		name  string
		phase planningPhase
	}{
		{name: "input phase", phase: phaseInput},
		{name: "progress phase", phase: phaseProgress},
		{name: "complete phase", phase: phaseComplete},
		{name: "error phase", phase: phaseError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			overlay := NewPlanningOverlay()
			overlay.phase = tt.phase

			if tt.phase == phaseComplete {
				overlay.state.CreatedBeads = []domain.Task{
					{ID: "az-1", Title: "Epic"},
				}
			}

			if tt.phase == phaseError {
				overlay.state.Error = "Test error"
			}

			if tt.phase == phaseProgress {
				overlay.state.CurrentPlan = &domain.Plan{
					EpicTitle: "Test Epic",
					Summary:   "Test summary",
					Tasks: []domain.PlannedTask{
						{
							ID:             "task-1",
							Title:          "Test task",
							CanParallelize: true,
						},
					},
					ParallelizationScore: 80,
				}
			}

			view := overlay.View()
			assert.NotEmpty(t, view, "view should not be empty")
		})
	}
}

func TestPlanningOverlay_Title(t *testing.T) {
	overlay := NewPlanningOverlay()
	assert.Equal(t, "AI Planning", overlay.Title())
}

func TestPlanningOverlay_Size(t *testing.T) {
	tests := []struct {
		name       string
		phase      planningPhase
		wantWidth  int
		wantHeight int
	}{
		{
			name:       "input phase",
			phase:      phaseInput,
			wantWidth:  80,
			wantHeight: 28,
		},
		{
			name:       "progress phase",
			phase:      phaseProgress,
			wantWidth:  80,
			wantHeight: 35,
		},
		{
			name:       "complete phase",
			phase:      phaseComplete,
			wantWidth:  80,
			wantHeight: 25,
		},
		{
			name:       "error phase",
			phase:      phaseError,
			wantWidth:  80,
			wantHeight: 15,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			overlay := NewPlanningOverlay()
			overlay.phase = tt.phase

			width, height := overlay.Size()
			assert.Equal(t, tt.wantWidth, width)
			assert.Equal(t, tt.wantHeight, height)
		})
	}
}

func TestTruncateText(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "shorter than max",
			input:  "hello",
			maxLen: 10,
			want:   "hello",
		},
		{
			name:   "exactly max",
			input:  "hello",
			maxLen: 5,
			want:   "hello",
		},
		{
			name:   "longer than max",
			input:  "hello world",
			maxLen: 8,
			want:   "hello...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateText(tt.input, tt.maxLen)
			assert.Equal(t, tt.want, result)
		})
	}
}
