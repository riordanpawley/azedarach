package domain

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	BoardViewDefinitionSchemaVersion = 1
	DefaultBoardViewID               = "default"
)

type BoardViewPredicateType string

const (
	BoardViewPredicateDisplayPhase    BoardViewPredicateType = "display_phase"
	BoardViewPredicateLifecycle       BoardViewPredicateType = "lifecycle_state"
	BoardViewPredicateReviewState     BoardViewPredicateType = "review_state"
	BoardViewPredicateClosedOutcome   BoardViewPredicateType = "closed_outcome"
	BoardViewPredicateSessionState    BoardViewPredicateType = "session_state"
	BoardViewPredicateSessionActivity BoardViewPredicateType = "session_activity"
	BoardViewPredicateHasSession      BoardViewPredicateType = "has_session"
	BoardViewPredicateWaitingHuman    BoardViewPredicateType = "waiting_human"
	BoardViewPredicateWaitingAI       BoardViewPredicateType = "waiting_ai"
	BoardViewPredicateAny             BoardViewPredicateType = "any"
	BoardViewPredicateAll             BoardViewPredicateType = "all"
	BoardViewPredicateNot             BoardViewPredicateType = "not"
)

type BoardViewDefinition struct {
	SchemaVersion int                         `json:"schema_version"`
	ID            string                      `json:"id"`
	Name          string                      `json:"name"`
	Columns       []BoardViewColumnDefinition `json:"columns"`
	Options       BoardViewDisplayOptions     `json:"options,omitempty"`
}

type BoardViewDisplayOptions struct {
	HideEmptyColumns bool `json:"hide_empty_columns,omitempty"`
}

type BoardViewColumnDefinition struct {
	ID        string                   `json:"id"`
	Title     string                   `json:"title"`
	Predicate BoardViewColumnPredicate `json:"predicate"`
}

type BoardViewColumnPredicate struct {
	Type          BoardViewPredicateType     `json:"type"`
	DisplayPhase  IssueDisplayPhase          `json:"display_phase,omitempty"`
	Lifecycle     IssueWorkflow              `json:"lifecycle_state,omitempty"`
	ReviewState   IssueReviewState           `json:"review_state,omitempty"`
	ClosedOutcome IssueCloseOutcome          `json:"closed_outcome,omitempty"`
	SessionStates []SessionState             `json:"session_states,omitempty"`
	Activities    []string                   `json:"activities,omitempty"`
	HasSession    *bool                      `json:"has_session,omitempty"`
	Any           []BoardViewColumnPredicate `json:"any,omitempty"`
	All           []BoardViewColumnPredicate `json:"all,omitempty"`
	Not           *BoardViewColumnPredicate  `json:"not,omitempty"`
}

type BoardViewRecord struct {
	ProjectID string              `json:"project_id"`
	View      BoardViewDefinition `json:"view"`
	BuiltIn   bool                `json:"built_in"`
	CreatedAt time.Time           `json:"created_at"`
	UpdatedAt time.Time           `json:"updated_at"`
}

type BoardViewColumnSnapshot struct {
	Definition BoardViewColumnDefinition `json:"definition"`
	Tasks      []Task                    `json:"tasks"`
}

func DecodeBoardViewDefinitionJSON(data []byte) (BoardViewDefinition, error) {
	var view BoardViewDefinition
	if err := json.Unmarshal(data, &view); err != nil {
		return BoardViewDefinition{}, err
	}
	if err := view.Validate(); err != nil {
		return BoardViewDefinition{}, err
	}
	return view.Normalized(), nil
}

func EncodeBoardViewDefinitionJSON(view BoardViewDefinition) ([]byte, error) {
	view = view.Normalized()
	if err := view.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(view)
}

func (v BoardViewDefinition) Normalized() BoardViewDefinition {
	v.ID = NormalizeBoardViewID(v.ID)
	v.Name = strings.TrimSpace(v.Name)
	if v.SchemaVersion == 0 {
		v.SchemaVersion = BoardViewDefinitionSchemaVersion
	}
	for i := range v.Columns {
		v.Columns[i].ID = NormalizeBoardViewID(v.Columns[i].ID)
		v.Columns[i].Title = strings.TrimSpace(v.Columns[i].Title)
		v.Columns[i].Predicate = v.Columns[i].Predicate.Normalized()
	}
	return v
}

func (v BoardViewDefinition) Validate() error {
	v = v.Normalized()
	if v.SchemaVersion != BoardViewDefinitionSchemaVersion {
		return fmt.Errorf("unsupported board view schema_version %d", v.SchemaVersion)
	}
	if v.ID == "" {
		return fmt.Errorf("board view id is required")
	}
	if v.Name == "" {
		return fmt.Errorf("board view name is required")
	}
	if len(v.Columns) == 0 {
		return fmt.Errorf("board view must define at least one column")
	}
	seen := map[string]bool{}
	for i, column := range v.Columns {
		if column.ID == "" {
			return fmt.Errorf("board view column %d id is required", i)
		}
		if seen[column.ID] {
			return fmt.Errorf("board view column id %q is duplicated", column.ID)
		}
		seen[column.ID] = true
		if column.Title == "" {
			return fmt.Errorf("board view column %q title is required", column.ID)
		}
		if err := column.Predicate.Validate(); err != nil {
			return fmt.Errorf("board view column %q predicate: %w", column.ID, err)
		}
	}
	return nil
}

func (p BoardViewColumnPredicate) Normalized() BoardViewColumnPredicate {
	p.Type = BoardViewPredicateType(strings.TrimSpace(string(p.Type)))
	for i := range p.Activities {
		p.Activities[i] = strings.ToLower(strings.TrimSpace(p.Activities[i]))
	}
	p.Activities = compactNonEmptyStrings(p.Activities)
	for i := range p.Any {
		p.Any[i] = p.Any[i].Normalized()
	}
	for i := range p.All {
		p.All[i] = p.All[i].Normalized()
	}
	if p.Not != nil {
		normalized := p.Not.Normalized()
		p.Not = &normalized
	}
	return p
}

func (p BoardViewColumnPredicate) Validate() error {
	p = p.Normalized()
	switch p.Type {
	case BoardViewPredicateDisplayPhase:
		if !validIssueDisplayPhase(p.DisplayPhase) {
			return fmt.Errorf("display_phase must be a known issue display phase")
		}
	case BoardViewPredicateLifecycle:
		if !validIssueWorkflow(p.Lifecycle) {
			return fmt.Errorf("lifecycle_state must be a known lifecycle state")
		}
	case BoardViewPredicateReviewState:
		if !validIssueReviewState(p.ReviewState) {
			return fmt.Errorf("review_state must be a known review state")
		}
	case BoardViewPredicateClosedOutcome:
		if !validIssueCloseOutcome(p.ClosedOutcome) {
			return fmt.Errorf("closed_outcome must be a known close outcome")
		}
	case BoardViewPredicateSessionState:
		if len(p.SessionStates) == 0 {
			return fmt.Errorf("session_state predicate requires at least one session state")
		}
		for _, state := range p.SessionStates {
			if !validSessionState(state) {
				return fmt.Errorf("unknown session state %q", state)
			}
		}
	case BoardViewPredicateSessionActivity:
		if len(p.Activities) == 0 {
			return fmt.Errorf("session_activity predicate requires at least one activity")
		}
	case BoardViewPredicateHasSession:
		if p.HasSession == nil {
			return fmt.Errorf("has_session predicate requires has_session")
		}
	case BoardViewPredicateWaitingHuman, BoardViewPredicateWaitingAI:
		return nil
	case BoardViewPredicateAny:
		if len(p.Any) == 0 {
			return fmt.Errorf("any predicate requires children")
		}
		for i := range p.Any {
			if err := p.Any[i].Validate(); err != nil {
				return fmt.Errorf("any[%d]: %w", i, err)
			}
		}
	case BoardViewPredicateAll:
		if len(p.All) == 0 {
			return fmt.Errorf("all predicate requires children")
		}
		for i := range p.All {
			if err := p.All[i].Validate(); err != nil {
				return fmt.Errorf("all[%d]: %w", i, err)
			}
		}
	case BoardViewPredicateNot:
		if p.Not == nil {
			return fmt.Errorf("not predicate requires child")
		}
		if err := p.Not.Validate(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown predicate type %q", p.Type)
	}
	return nil
}

func (p BoardViewColumnPredicate) Matches(task Task) bool {
	p = p.Normalized()
	switch p.Type {
	case BoardViewPredicateDisplayPhase:
		return task.IssueDisplayPhase() == p.DisplayPhase
	case BoardViewPredicateLifecycle:
		state, err := task.IssueState()
		return err == nil && state.Workflow() == p.Lifecycle
	case BoardViewPredicateReviewState:
		state, err := task.IssueState()
		return err == nil && state.Review() == p.ReviewState
	case BoardViewPredicateClosedOutcome:
		state, err := task.IssueState()
		return err == nil && state.CloseOutcome() == p.ClosedOutcome
	case BoardViewPredicateSessionState:
		if task.Session == nil {
			return false
		}
		for _, state := range p.SessionStates {
			if task.Session.State == state {
				return true
			}
			if displayState, ok := task.Session.DisplayState(); ok && displayState == state {
				return true
			}
		}
		return false
	case BoardViewPredicateSessionActivity:
		if task.Session == nil {
			return false
		}
		activity := strings.ToLower(strings.TrimSpace(task.Session.DisplayActivity()))
		if activity == "" {
			activity = strings.ToLower(strings.TrimSpace(task.Session.Activity))
		}
		for _, want := range p.Activities {
			if activity == want {
				return true
			}
		}
		return false
	case BoardViewPredicateHasSession:
		has := task.HasTmuxSession || task.Session != nil
		return p.HasSession != nil && has == *p.HasSession
	case BoardViewPredicateWaitingHuman:
		return taskSessionActivityIn(task, "waiting_human", "waiting-for-human")
	case BoardViewPredicateWaitingAI:
		return taskSessionActivityIn(task, "waiting_ai", "waiting-for-ai", "waiting_tool", "waiting-for-tool")
	case BoardViewPredicateAny:
		for _, child := range p.Any {
			if child.Matches(task) {
				return true
			}
		}
		return false
	case BoardViewPredicateAll:
		for _, child := range p.All {
			if !child.Matches(task) {
				return false
			}
		}
		return true
	case BoardViewPredicateNot:
		return p.Not != nil && !p.Not.Matches(task)
	default:
		return false
	}
}

func GroupTasksByBoardView(view BoardViewDefinition, tasks []Task) ([]BoardViewColumnSnapshot, error) {
	view = view.Normalized()
	if err := view.Validate(); err != nil {
		return nil, err
	}
	columns := make([]BoardViewColumnSnapshot, 0, len(view.Columns))
	for _, column := range view.Columns {
		columns = append(columns, BoardViewColumnSnapshot{Definition: column})
	}
	for _, task := range tasks {
		for i, column := range view.Columns {
			if column.Predicate.Matches(task) {
				columns[i].Tasks = append(columns[i].Tasks, task)
				break
			}
		}
	}
	if view.Options.HideEmptyColumns {
		filtered := columns[:0]
		for _, column := range columns {
			if len(column.Tasks) > 0 {
				filtered = append(filtered, column)
			}
		}
		columns = filtered
	}
	return columns, nil
}

func BuiltInBoardViews() []BoardViewDefinition {
	return []BoardViewDefinition{DefaultBoardView()}
}

func DefaultBoardView() BoardViewDefinition {
	return BoardViewDefinition{
		SchemaVersion: BoardViewDefinitionSchemaVersion,
		ID:            DefaultBoardViewID,
		Name:          "Default",
		Columns: []BoardViewColumnDefinition{
			displayPhaseColumn(IssueDisplayBacklog, "Backlog"),
			displayPhaseColumn(IssueDisplayOpen, "Open"),
			displayPhaseColumn(IssueDisplayActive, "Active"),
			displayPhaseColumn(IssueDisplayReview, "Review"),
			displayPhaseColumn(IssueDisplayDone, "Done"),
			displayPhaseColumn(IssueDisplayCancelled, "Cancelled"),
		},
	}
}

func displayPhaseColumn(phase IssueDisplayPhase, title string) BoardViewColumnDefinition {
	return BoardViewColumnDefinition{
		ID:    string(phase),
		Title: title,
		Predicate: BoardViewColumnPredicate{
			Type:         BoardViewPredicateDisplayPhase,
			DisplayPhase: phase,
		},
	}
}

func NormalizeBoardViewID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "-")
	return value
}

func compactNonEmptyStrings(values []string) []string {
	out := values[:0]
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func taskSessionActivityIn(task Task, values ...string) bool {
	if task.Session == nil {
		return false
	}
	activity := strings.ToLower(strings.TrimSpace(task.Session.DisplayActivity()))
	if activity == "" {
		activity = strings.ToLower(strings.TrimSpace(task.Session.Activity))
	}
	for _, value := range values {
		if activity == value {
			return true
		}
	}
	return false
}

func validIssueDisplayPhase(value IssueDisplayPhase) bool {
	switch value {
	case IssueDisplayBacklog, IssueDisplayOpen, IssueDisplayActive, IssueDisplayReview, IssueDisplayDone, IssueDisplayCancelled:
		return true
	default:
		return false
	}
}

func validIssueWorkflow(value IssueWorkflow) bool {
	switch value {
	case IssueWorkflowBacklog, IssueWorkflowOpen, IssueWorkflowActive, IssueWorkflowClosed:
		return true
	default:
		return false
	}
}

func validIssueReviewState(value IssueReviewState) bool {
	switch value {
	case IssueReviewNone, IssueReviewRequested:
		return true
	default:
		return false
	}
}

func validIssueCloseOutcome(value IssueCloseOutcome) bool {
	switch value {
	case IssueCloseNone, IssueCloseCompleted, IssueCloseCancelled:
		return true
	default:
		return false
	}
}

func validSessionState(value SessionState) bool {
	switch value {
	case SessionIdle, SessionBusy, SessionWaiting, SessionDone, SessionError, SessionPaused:
		return true
	default:
		return false
	}
}
