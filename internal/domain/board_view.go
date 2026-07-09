package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type BoardViewID string

const (
	BoardViewCurrentID  BoardViewID = "current"
	BoardViewActivityID BoardViewID = "activity"
)

type BoardColumnID string

const (
	BoardColumnBacklog      BoardColumnID = "backlog"
	BoardColumnOpen         BoardColumnID = "open"
	BoardColumnActive       BoardColumnID = "active"
	BoardColumnReviewReady  BoardColumnID = "review_ready"
	BoardColumnDone         BoardColumnID = "done"
	BoardColumnCancelled    BoardColumnID = "cancelled"
	BoardColumnWaitingHuman BoardColumnID = "waiting_human"
	BoardColumnWaitingAI    BoardColumnID = "waiting_ai"
)

type BoardColumnPredicateKind string

const (
	BoardPredicateLifecycle          BoardColumnPredicateKind = "lifecycle"
	BoardPredicateDisplayPhase       BoardColumnPredicateKind = "display_phase"
	BoardPredicateReviewReady        BoardColumnPredicateKind = "review_ready"
	BoardPredicateClosedOutcome      BoardColumnPredicateKind = "closed_outcome"
	BoardPredicateWaitingHuman       BoardColumnPredicateKind = "waiting_human"
	BoardPredicateWaitingAIDelegated BoardColumnPredicateKind = "waiting_ai_delegated"
)

type BoardViewSet struct {
	DefaultViewID BoardViewID `json:"default_view_id"`
	Views         []BoardView `json:"views"`
}

type BoardView struct {
	ID      BoardViewID   `json:"id"`
	Title   string        `json:"title"`
	Columns []BoardColumn `json:"columns"`
}

type BoardColumn struct {
	ID         BoardColumnID          `json:"id"`
	Title      string                 `json:"title"`
	Predicates []BoardColumnPredicate `json:"predicates"`
}

type BoardColumnPredicate struct {
	Kind           BoardColumnPredicateKind `json:"kind"`
	Lifecycle      []IssueWorkflow          `json:"lifecycle,omitempty"`
	DisplayPhases  []IssueDisplayPhase      `json:"display_phases,omitempty"`
	ClosedOutcomes []IssueCloseOutcome      `json:"closed_outcomes,omitempty"`
}

type BoardPlacement struct {
	Matched        bool          `json:"matched"`
	ViewID         BoardViewID   `json:"view_id,omitempty"`
	ColumnID       BoardColumnID `json:"column_id,omitempty"`
	ColumnTitle    string        `json:"column_title,omitempty"`
	ColumnIndex    int           `json:"column_index"`
	MatchReason    string        `json:"match_reason"`
	PredicateKinds []string      `json:"predicate_kinds,omitempty"`
}

func BuiltInBoardViewSet() BoardViewSet {
	return BoardViewSet{
		DefaultViewID: BoardViewCurrentID,
		Views: []BoardView{
			CurrentBoardView(),
			ActivityBoardView(),
		},
	}
}

func CurrentBoardView() BoardView {
	return BoardView{
		ID:    BoardViewCurrentID,
		Title: "Current",
		Columns: []BoardColumn{
			{
				ID:    BoardColumnBacklog,
				Title: "Backlog",
				Predicates: []BoardColumnPredicate{{
					Kind:      BoardPredicateLifecycle,
					Lifecycle: []IssueWorkflow{IssueWorkflowBacklog},
				}},
			},
			{
				ID:    BoardColumnOpen,
				Title: "Open",
				Predicates: []BoardColumnPredicate{{
					Kind:      BoardPredicateLifecycle,
					Lifecycle: []IssueWorkflow{IssueWorkflowOpen},
				}},
			},
			{
				ID:    BoardColumnActive,
				Title: "In Progress",
				Predicates: []BoardColumnPredicate{{
					Kind:          BoardPredicateDisplayPhase,
					DisplayPhases: []IssueDisplayPhase{IssueDisplayActive},
				}},
			},
			{
				ID:    BoardColumnReviewReady,
				Title: "In Review",
				Predicates: []BoardColumnPredicate{{
					Kind: BoardPredicateReviewReady,
				}},
			},
			{
				ID:    BoardColumnDone,
				Title: "Done",
				Predicates: []BoardColumnPredicate{{
					Kind:           BoardPredicateClosedOutcome,
					ClosedOutcomes: []IssueCloseOutcome{IssueCloseCompleted},
				}},
			},
			{
				ID:    BoardColumnCancelled,
				Title: "Cancelled",
				Predicates: []BoardColumnPredicate{{
					Kind:           BoardPredicateClosedOutcome,
					ClosedOutcomes: []IssueCloseOutcome{IssueCloseCancelled},
				}},
			},
		},
	}
}

func ActivityBoardView() BoardView {
	view := CurrentBoardView()
	view.ID = BoardViewActivityID
	view.Title = "Activity"
	view.Columns = []BoardColumn{
		view.Columns[0],
		view.Columns[1],
		{
			ID:    BoardColumnWaitingHuman,
			Title: "Waiting Human",
			Predicates: []BoardColumnPredicate{{
				Kind: BoardPredicateWaitingHuman,
			}},
		},
		{
			ID:    BoardColumnWaitingAI,
			Title: "Waiting AI",
			Predicates: []BoardColumnPredicate{{
				Kind: BoardPredicateWaitingAIDelegated,
			}},
		},
		view.Columns[2],
		view.Columns[3],
		view.Columns[4],
		view.Columns[5],
	}
	return view
}

func (s BoardViewSet) Validate() error {
	if strings.TrimSpace(string(s.DefaultViewID)) == "" {
		return fmt.Errorf("board view set default view id is required")
	}
	if len(s.Views) == 0 {
		return fmt.Errorf("board view set must define at least one view")
	}
	seen := make(map[BoardViewID]struct{}, len(s.Views))
	defaultFound := false
	for i, view := range s.Views {
		if err := view.Validate(); err != nil {
			return fmt.Errorf("view %d: %w", i, err)
		}
		if _, ok := seen[view.ID]; ok {
			return fmt.Errorf("duplicate board view id %q", view.ID)
		}
		seen[view.ID] = struct{}{}
		if view.ID == s.DefaultViewID {
			defaultFound = true
		}
	}
	if !defaultFound {
		return fmt.Errorf("default board view %q is unknown", s.DefaultViewID)
	}
	return nil
}

func (s BoardViewSet) ViewByID(id BoardViewID) (BoardView, bool) {
	for _, view := range s.Views {
		if view.ID == id {
			return view, true
		}
	}
	return BoardView{}, false
}

func (v BoardView) Validate() error {
	if strings.TrimSpace(string(v.ID)) == "" {
		return fmt.Errorf("board view id is required")
	}
	if strings.TrimSpace(v.Title) == "" {
		return fmt.Errorf("board view %q title is required", v.ID)
	}
	if len(v.Columns) == 0 {
		return fmt.Errorf("board view %q must define at least one column", v.ID)
	}
	seen := make(map[BoardColumnID]struct{}, len(v.Columns))
	for i, column := range v.Columns {
		if err := column.Validate(); err != nil {
			return fmt.Errorf("column %d: %w", i, err)
		}
		if _, ok := seen[column.ID]; ok {
			return fmt.Errorf("duplicate board column id %q", column.ID)
		}
		seen[column.ID] = struct{}{}
	}
	return nil
}

func (c BoardColumn) Validate() error {
	if strings.TrimSpace(string(c.ID)) == "" {
		return fmt.Errorf("board column id is required")
	}
	if !knownBoardColumnID(c.ID) {
		return fmt.Errorf("unknown board column id %q", c.ID)
	}
	if strings.TrimSpace(c.Title) == "" {
		return fmt.Errorf("board column %q title is required", c.ID)
	}
	if len(c.Predicates) == 0 {
		return fmt.Errorf("board column %q must define at least one predicate", c.ID)
	}
	if err := validateBoardColumnPredicateCombination(c.Predicates); err != nil {
		return fmt.Errorf("board column %q predicate combination: %w", c.ID, err)
	}
	for i, predicate := range c.Predicates {
		if err := predicate.Validate(); err != nil {
			return fmt.Errorf("board column %q predicate %d: %w", c.ID, i, err)
		}
	}
	return nil
}

func (p *BoardColumnPredicate) UnmarshalJSON(data []byte) error {
	type predicate BoardColumnPredicate
	var decoded predicate
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&decoded); err != nil {
		return err
	}
	out := BoardColumnPredicate(decoded)
	if err := out.Validate(); err != nil {
		return err
	}
	*p = out
	return nil
}

func (p BoardColumnPredicate) Validate() error {
	switch p.Kind {
	case BoardPredicateLifecycle:
		if len(p.Lifecycle) == 0 {
			return fmt.Errorf("lifecycle predicate requires at least one lifecycle")
		}
		if len(p.DisplayPhases) > 0 || len(p.ClosedOutcomes) > 0 {
			return fmt.Errorf("lifecycle predicate cannot carry display phase or closed outcome payloads")
		}
		return validateIssueWorkflows(p.Lifecycle)
	case BoardPredicateDisplayPhase:
		if len(p.DisplayPhases) == 0 {
			return fmt.Errorf("display phase predicate requires at least one display phase")
		}
		if len(p.Lifecycle) > 0 || len(p.ClosedOutcomes) > 0 {
			return fmt.Errorf("display phase predicate cannot carry lifecycle or closed outcome payloads")
		}
		return validateIssueDisplayPhases(p.DisplayPhases)
	case BoardPredicateClosedOutcome:
		if len(p.ClosedOutcomes) == 0 {
			return fmt.Errorf("closed outcome predicate requires at least one outcome")
		}
		if len(p.Lifecycle) > 0 || len(p.DisplayPhases) > 0 {
			return fmt.Errorf("closed outcome predicate cannot carry lifecycle or display phase payloads")
		}
		return validateIssueCloseOutcomes(p.ClosedOutcomes)
	case BoardPredicateReviewReady, BoardPredicateWaitingHuman, BoardPredicateWaitingAIDelegated:
		if len(p.Lifecycle) > 0 || len(p.DisplayPhases) > 0 || len(p.ClosedOutcomes) > 0 {
			return fmt.Errorf("%s predicate cannot carry payload fields", p.Kind)
		}
		return nil
	default:
		return fmt.Errorf("unsupported board column predicate kind %q", p.Kind)
	}
}

func (v BoardView) PlaceTask(task Task) (BoardPlacement, error) {
	if err := v.Validate(); err != nil {
		return BoardPlacement{}, err
	}
	for i, column := range v.Columns {
		matched, reasons := column.MatchTask(task)
		if !matched {
			continue
		}
		kinds := make([]string, 0, len(column.Predicates))
		for _, predicate := range column.Predicates {
			kinds = append(kinds, string(predicate.Kind))
		}
		return BoardPlacement{
			Matched:        true,
			ViewID:         v.ID,
			ColumnID:       column.ID,
			ColumnTitle:    column.Title,
			ColumnIndex:    i,
			MatchReason:    strings.Join(reasons, "; "),
			PredicateKinds: kinds,
		}, nil
	}
	return BoardPlacement{
		Matched:     false,
		ViewID:      v.ID,
		ColumnIndex: -1,
		MatchReason: "no board column predicates matched",
	}, nil
}

func (c BoardColumn) MatchTask(task Task) (bool, []string) {
	reasons := make([]string, 0, len(c.Predicates))
	for _, predicate := range c.Predicates {
		matched, reason := predicate.MatchTask(task)
		if !matched {
			return false, nil
		}
		reasons = append(reasons, reason)
	}
	return true, reasons
}

func (p BoardColumnPredicate) MatchTask(task Task) (bool, string) {
	switch p.Kind {
	case BoardPredicateLifecycle:
		state, err := task.IssueState()
		if err != nil {
			return false, "invalid issue state"
		}
		if issueWorkflowIn(state.Workflow(), p.Lifecycle) {
			return true, fmt.Sprintf("lifecycle=%s", state.Workflow())
		}
		return false, fmt.Sprintf("lifecycle=%s not in %s", state.Workflow(), joinStringers(p.Lifecycle))
	case BoardPredicateDisplayPhase:
		phase := task.IssueDisplayPhase()
		if issueDisplayPhaseIn(phase, p.DisplayPhases) {
			return true, fmt.Sprintf("display_phase=%s", phase)
		}
		return false, fmt.Sprintf("display_phase=%s not in %s", phase, joinStringers(p.DisplayPhases))
	case BoardPredicateReviewReady:
		if taskReviewReady(task) {
			return true, "review_ready=true"
		}
		return false, "review_ready=false"
	case BoardPredicateClosedOutcome:
		state, err := task.IssueState()
		if err != nil {
			return false, "invalid issue state"
		}
		if issueCloseOutcomeIn(state.CloseOutcome(), p.ClosedOutcomes) {
			return true, fmt.Sprintf("closed_outcome=%s", state.CloseOutcome())
		}
		return false, fmt.Sprintf("closed_outcome=%s not in %s", state.CloseOutcome(), joinStringers(p.ClosedOutcomes))
	case BoardPredicateWaitingHuman:
		if sessionWaitingOnHuman(task.Session) {
			return true, "waiting_human=true"
		}
		return false, "waiting_human=false"
	case BoardPredicateWaitingAIDelegated:
		if sessionWaitingOnAIDelegated(task.Session) {
			return true, "waiting_ai_delegated=true"
		}
		return false, "waiting_ai_delegated=false"
	default:
		return false, fmt.Sprintf("unsupported predicate kind=%s", p.Kind)
	}
}

func taskReviewReady(task Task) bool {
	state, err := task.IssueState()
	if err != nil {
		return false
	}
	return state.Workflow() == IssueWorkflowActive &&
		state.Review() == IssueReviewRequested &&
		task.Session.AllowsReviewReadyPhase(task.HasTmuxSession)
}

func validateBoardColumnPredicateCombination(predicates []BoardColumnPredicate) error {
	lifecycles := map[IssueWorkflow]struct{}{}
	displayPhases := map[IssueDisplayPhase]struct{}{}
	closedOutcomes := map[IssueCloseOutcome]struct{}{}
	hasLifecycle := false
	hasDisplayPhase := false
	hasClosedOutcome := false
	hasReviewReady := false
	hasWaitingHuman := false
	hasWaitingAI := false

	for _, predicate := range predicates {
		if err := predicate.Validate(); err != nil {
			return err
		}
		switch predicate.Kind {
		case BoardPredicateLifecycle:
			hasLifecycle = true
			for _, lifecycle := range predicate.Lifecycle {
				lifecycles[lifecycle] = struct{}{}
			}
		case BoardPredicateDisplayPhase:
			hasDisplayPhase = true
			for _, phase := range predicate.DisplayPhases {
				displayPhases[phase] = struct{}{}
			}
		case BoardPredicateClosedOutcome:
			hasClosedOutcome = true
			for _, outcome := range predicate.ClosedOutcomes {
				closedOutcomes[outcome] = struct{}{}
			}
		case BoardPredicateReviewReady:
			hasReviewReady = true
		case BoardPredicateWaitingHuman:
			hasWaitingHuman = true
		case BoardPredicateWaitingAIDelegated:
			hasWaitingAI = true
		}
	}

	if hasClosedOutcome {
		if hasLifecycle && !mapHasWorkflow(lifecycles, IssueWorkflowClosed) {
			return fmt.Errorf("closed outcome requires lifecycle %s", IssueWorkflowClosed)
		}
		if hasDisplayPhase && !closedOutcomesDisplayPhasesOverlap(closedOutcomes, displayPhases) {
			return fmt.Errorf("closed outcome and display phase predicates cannot match the same issue")
		}
		if hasReviewReady {
			return fmt.Errorf("closed outcome cannot be combined with review-ready")
		}
		if hasWaitingHuman || hasWaitingAI {
			return fmt.Errorf("closed outcome cannot be combined with waiting predicates")
		}
	}
	if hasReviewReady {
		if hasLifecycle && !mapHasWorkflow(lifecycles, IssueWorkflowActive) {
			return fmt.Errorf("review-ready requires lifecycle %s", IssueWorkflowActive)
		}
		if hasDisplayPhase && !mapHasDisplayPhase(displayPhases, IssueDisplayReview) {
			return fmt.Errorf("review-ready requires display phase %s", IssueDisplayReview)
		}
	}
	if hasWaitingHuman && hasWaitingAI {
		return fmt.Errorf("waiting-human and waiting-ai delegated predicates are mutually exclusive")
	}
	if (hasWaitingHuman || hasWaitingAI) && hasLifecycle && mapOnlyWorkflow(lifecycles, IssueWorkflowClosed) {
		return fmt.Errorf("waiting predicates cannot match only closed lifecycle")
	}
	if hasLifecycle && hasDisplayPhase && !lifecycleDisplayPhasesOverlap(lifecycles, displayPhases) {
		return fmt.Errorf("lifecycle and display phase predicates cannot match the same issue")
	}
	return nil
}

func validateIssueWorkflows(values []IssueWorkflow) error {
	seen := map[IssueWorkflow]struct{}{}
	for _, value := range values {
		switch value {
		case IssueWorkflowBacklog, IssueWorkflowOpen, IssueWorkflowActive, IssueWorkflowClosed:
		default:
			return fmt.Errorf("invalid issue workflow %q", value)
		}
		if _, ok := seen[value]; ok {
			return fmt.Errorf("duplicate issue workflow %q", value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateIssueDisplayPhases(values []IssueDisplayPhase) error {
	seen := map[IssueDisplayPhase]struct{}{}
	for _, value := range values {
		switch value {
		case IssueDisplayBacklog, IssueDisplayOpen, IssueDisplayActive, IssueDisplayReview, IssueDisplayDone, IssueDisplayCancelled:
		default:
			return fmt.Errorf("invalid issue display phase %q", value)
		}
		if _, ok := seen[value]; ok {
			return fmt.Errorf("duplicate issue display phase %q", value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateIssueCloseOutcomes(values []IssueCloseOutcome) error {
	seen := map[IssueCloseOutcome]struct{}{}
	for _, value := range values {
		switch value {
		case IssueCloseCompleted, IssueCloseCancelled:
		default:
			return fmt.Errorf("invalid issue close outcome %q", value)
		}
		if _, ok := seen[value]; ok {
			return fmt.Errorf("duplicate issue close outcome %q", value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func knownBoardColumnID(id BoardColumnID) bool {
	switch id {
	case BoardColumnBacklog,
		BoardColumnOpen,
		BoardColumnActive,
		BoardColumnReviewReady,
		BoardColumnDone,
		BoardColumnCancelled,
		BoardColumnWaitingHuman,
		BoardColumnWaitingAI:
		return true
	default:
		return false
	}
}

func issueWorkflowIn(value IssueWorkflow, values []IssueWorkflow) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func issueDisplayPhaseIn(value IssueDisplayPhase, values []IssueDisplayPhase) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func issueCloseOutcomeIn(value IssueCloseOutcome, values []IssueCloseOutcome) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func mapHasWorkflow(values map[IssueWorkflow]struct{}, target IssueWorkflow) bool {
	_, ok := values[target]
	return ok
}

func mapOnlyWorkflow(values map[IssueWorkflow]struct{}, target IssueWorkflow) bool {
	return len(values) == 1 && mapHasWorkflow(values, target)
}

func mapHasDisplayPhase(values map[IssueDisplayPhase]struct{}, target IssueDisplayPhase) bool {
	_, ok := values[target]
	return ok
}

func lifecycleDisplayPhasesOverlap(lifecycles map[IssueWorkflow]struct{}, phases map[IssueDisplayPhase]struct{}) bool {
	allowed := map[IssueWorkflow][]IssueDisplayPhase{
		IssueWorkflowBacklog: {IssueDisplayBacklog},
		IssueWorkflowOpen:    {IssueDisplayOpen},
		IssueWorkflowActive:  {IssueDisplayActive, IssueDisplayReview},
		IssueWorkflowClosed:  {IssueDisplayDone, IssueDisplayCancelled},
	}
	for lifecycle := range lifecycles {
		for _, phase := range allowed[lifecycle] {
			if mapHasDisplayPhase(phases, phase) {
				return true
			}
		}
	}
	return false
}

func closedOutcomesDisplayPhasesOverlap(outcomes map[IssueCloseOutcome]struct{}, phases map[IssueDisplayPhase]struct{}) bool {
	allowed := map[IssueCloseOutcome]IssueDisplayPhase{
		IssueCloseCompleted: IssueDisplayDone,
		IssueCloseCancelled: IssueDisplayCancelled,
	}
	for outcome := range outcomes {
		if mapHasDisplayPhase(phases, allowed[outcome]) {
			return true
		}
	}
	return false
}

func sessionWaitingOnHuman(session *Session) bool {
	if session == nil {
		return false
	}
	switch normalizedBoardSessionActivity(session) {
	case string(SessionWaiting), "waiting_human", "waiting-for-human":
		return true
	default:
		return false
	}
}

func sessionWaitingOnAIDelegated(session *Session) bool {
	if session == nil {
		return false
	}
	switch normalizedBoardSessionActivity(session) {
	case "waiting_ai", "waiting-for-ai", "waiting_tool", "waiting-for-tool":
		return true
	default:
		return false
	}
}

func normalizedBoardSessionActivity(session *Session) string {
	if session == nil {
		return ""
	}
	if activity := strings.ToLower(strings.TrimSpace(session.DisplayActivity())); activity != "" {
		return activity
	}
	return strings.ToLower(strings.TrimSpace(string(session.State)))
}

func joinStringers[T ~string](values []T) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, string(value))
	}
	return strings.Join(parts, ",")
}
