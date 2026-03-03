package tasks

import (
	"errors"
	"fmt"
)

type FlowMode string

const (
	FlowModeManual FlowMode = "manual"
	FlowModeAI     FlowMode = "ai"
)

type FlowAction string

const (
	FlowActionCreate FlowAction = "create"
	FlowActionEdit   FlowAction = "edit"
	FlowActionFork   FlowAction = "fork"
)

type FlowState string

const (
	FlowStateDraft      FlowState = "draft"
	FlowStateValidating FlowState = "validating"
	FlowStateReady      FlowState = "ready"
	FlowStateSubmitting FlowState = "submitting"
	FlowStateCompleted  FlowState = "completed"
	FlowStateFailed     FlowState = "failed"
	FlowStateCancelled  FlowState = "cancelled"
)

type CreateEditInput struct {
	TaskID      string
	SourceTask  string
	Title       string
	Description string
	AIPrompt    string
}

type CreateEditModel struct {
	mode   FlowMode
	action FlowAction
	state  FlowState
	input  CreateEditInput
}

var (
	ErrInvalidFlowMode       = errors.New("invalid flow mode")
	ErrInvalidFlowAction     = errors.New("invalid flow action")
	ErrInvalidFlowState      = errors.New("invalid flow state")
	ErrInvalidFlowTransition = errors.New("invalid flow transition")
	ErrTitleRequired         = errors.New("title is required")
	ErrTaskIDRequired        = errors.New("task id is required")
	ErrSourceTaskRequired    = errors.New("source task id is required")
	ErrAIPromptRequired      = errors.New("ai prompt is required")
	ErrNoChangesRequested    = errors.New("no edit changes requested")
)

func NewCreateEditModel(mode FlowMode, action FlowAction, input CreateEditInput) (CreateEditModel, error) {
	if !mode.valid() {
		return CreateEditModel{}, fmt.Errorf("%w: %s", ErrInvalidFlowMode, mode)
	}
	if !action.valid() {
		return CreateEditModel{}, fmt.Errorf("%w: %s", ErrInvalidFlowAction, action)
	}

	m := CreateEditModel{
		mode:   mode,
		action: action,
		state:  FlowStateDraft,
		input:  input,
	}

	if err := m.ValidateInput(); err != nil {
		return CreateEditModel{}, err
	}
	return m, nil
}

func (m CreateEditModel) Mode() FlowMode {
	return m.mode
}

func (m CreateEditModel) Action() FlowAction {
	return m.action
}

func (m CreateEditModel) State() FlowState {
	return m.state
}

func (m CreateEditModel) Input() CreateEditInput {
	return m.input
}

func (m CreateEditModel) ValidateInput() error {
	if m.mode == FlowModeAI && m.input.AIPrompt == "" {
		return ErrAIPromptRequired
	}

	switch m.action {
	case FlowActionCreate:
		if m.input.Title == "" {
			return ErrTitleRequired
		}
	case FlowActionEdit:
		if m.input.TaskID == "" {
			return ErrTaskIDRequired
		}
		if m.input.Title == "" && m.input.Description == "" && m.input.AIPrompt == "" {
			return ErrNoChangesRequested
		}
	case FlowActionFork:
		if m.input.SourceTask == "" {
			return ErrSourceTaskRequired
		}
		if m.input.Title == "" {
			return ErrTitleRequired
		}
	default:
		return fmt.Errorf("%w: %s", ErrInvalidFlowAction, m.action)
	}

	return nil
}

func (m CreateEditModel) AllowedTransitions() []FlowState {
	next := allowedTransitions[m.state]
	out := make([]FlowState, len(next))
	copy(out, next)
	return out
}

func (m CreateEditModel) Transition(next FlowState) (CreateEditModel, error) {
	if !next.valid() {
		return m, fmt.Errorf("%w: %s", ErrInvalidFlowState, next)
	}
	if m.state == next {
		return m, nil
	}

	if !containsState(allowedTransitions[m.state], next) {
		return m, fmt.Errorf("%w: %s -> %s", ErrInvalidFlowTransition, m.state, next)
	}

	if next == FlowStateValidating {
		if err := m.ValidateInput(); err != nil {
			return m, err
		}
	}

	m.state = next
	return m, nil
}

var allowedTransitions = map[FlowState][]FlowState{
	FlowStateDraft:      {FlowStateValidating, FlowStateCancelled},
	FlowStateValidating: {FlowStateReady, FlowStateFailed},
	FlowStateReady:      {FlowStateSubmitting, FlowStateCancelled},
	FlowStateSubmitting: {FlowStateCompleted, FlowStateFailed},
	FlowStateFailed:     {FlowStateDraft, FlowStateCancelled},
	FlowStateCompleted:  {},
	FlowStateCancelled:  {},
}

func (m FlowMode) valid() bool {
	switch m {
	case FlowModeManual, FlowModeAI:
		return true
	default:
		return false
	}
}

func (a FlowAction) valid() bool {
	switch a {
	case FlowActionCreate, FlowActionEdit, FlowActionFork:
		return true
	default:
		return false
	}
}

func (s FlowState) valid() bool {
	_, ok := allowedTransitions[s]
	return ok
}

func containsState(states []FlowState, target FlowState) bool {
	for _, state := range states {
		if state == target {
			return true
		}
	}
	return false
}
