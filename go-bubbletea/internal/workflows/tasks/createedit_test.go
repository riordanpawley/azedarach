package tasks

import (
	"errors"
	"reflect"
	"testing"
)

func TestCreateEditValidationMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mode    FlowMode
		action  FlowAction
		input   CreateEditInput
		wantErr error
	}{
		{
			name:   "manual create valid",
			mode:   FlowModeManual,
			action: FlowActionCreate,
			input:  CreateEditInput{Title: "Add feature"},
		},
		{
			name:    "create requires title",
			mode:    FlowModeManual,
			action:  FlowActionCreate,
			input:   CreateEditInput{},
			wantErr: ErrTitleRequired,
		},
		{
			name:   "ai edit valid",
			mode:   FlowModeAI,
			action: FlowActionEdit,
			input: CreateEditInput{
				TaskID:   "az-123",
				AIPrompt: "tighten acceptance criteria",
			},
		},
		{
			name:    "edit requires task id",
			mode:    FlowModeManual,
			action:  FlowActionEdit,
			input:   CreateEditInput{Description: "update details"},
			wantErr: ErrTaskIDRequired,
		},
		{
			name:    "manual edit requires change",
			mode:    FlowModeManual,
			action:  FlowActionEdit,
			input:   CreateEditInput{TaskID: "az-123"},
			wantErr: ErrNoChangesRequested,
		},
		{
			name:    "fork requires source",
			mode:    FlowModeManual,
			action:  FlowActionFork,
			input:   CreateEditInput{Title: "Forked follow-up"},
			wantErr: ErrSourceTaskRequired,
		},
		{
			name:    "ai mode requires prompt",
			mode:    FlowModeAI,
			action:  FlowActionCreate,
			input:   CreateEditInput{Title: "AI generated"},
			wantErr: ErrAIPromptRequired,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewCreateEditModel(tt.mode, tt.action, tt.input)
			if tt.wantErr == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestCreateEditTransitionsDeterministic(t *testing.T) {
	t.Parallel()

	model, err := NewCreateEditModel(
		FlowModeManual,
		FlowActionCreate,
		CreateEditInput{Title: "Create deterministic state machine"},
	)
	if err != nil {
		t.Fatalf("new create/edit model: %v", err)
	}

	if got, want := model.AllowedTransitions(), []FlowState{FlowStateValidating, FlowStateCancelled}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected draft transitions: got=%v want=%v", got, want)
	}

	model, err = model.Transition(FlowStateValidating)
	if err != nil {
		t.Fatalf("transition validating: %v", err)
	}
	model, err = model.Transition(FlowStateReady)
	if err != nil {
		t.Fatalf("transition ready: %v", err)
	}
	model, err = model.Transition(FlowStateSubmitting)
	if err != nil {
		t.Fatalf("transition submitting: %v", err)
	}
	model, err = model.Transition(FlowStateCompleted)
	if err != nil {
		t.Fatalf("transition completed: %v", err)
	}

	if model.State() != FlowStateCompleted {
		t.Fatalf("expected completed state, got %s", model.State())
	}

	if got := model.AllowedTransitions(); len(got) != 0 {
		t.Fatalf("expected no transitions from completed, got %v", got)
	}
}

func TestCreateEditInvalidTransitionsRejected(t *testing.T) {
	t.Parallel()

	model, err := NewCreateEditModel(
		FlowModeManual,
		FlowActionFork,
		CreateEditInput{SourceTask: "az-1", Title: "Fork title"},
	)
	if err != nil {
		t.Fatalf("new create/edit model: %v", err)
	}

	_, err = model.Transition(FlowStateReady)
	if !errors.Is(err, ErrInvalidFlowTransition) {
		t.Fatalf("expected invalid transition error, got %v", err)
	}
}
