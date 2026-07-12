package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

const BoardViewDefinitionSchemaVersion = 2

type BoardViewLayout string

const (
	BoardViewLayoutColumnBoard    BoardViewLayout = "column_board"
	BoardViewLayoutTreeList       BoardViewLayout = "tree_list"
	BoardViewLayoutHorizontalGrid BoardViewLayout = "horizontal_grid"
)

type BoardViewID string

const (
	BoardViewDefaultID       BoardViewID = "default"
	BoardViewPlanningID      BoardViewID = "planning"
	BoardViewOrchestrationID BoardViewID = "orchestration"
	BoardViewCloseoutID      BoardViewID = "closeout"
	BoardViewGridID          BoardViewID = "grid"
	BoardViewTreeID          BoardViewID = "tree"
	// Legacy IDs remain exported so callers can recognize pre-catalog selections.
	BoardViewCurrentID  BoardViewID = "current"
	BoardViewActivityID BoardViewID = "activity"
	DefaultBoardViewID              = string(BoardViewDefaultID)
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
	ID      BoardViewID             `json:"id"`
	Title   string                  `json:"title"`
	Layout  BoardViewLayout         `json:"layout,omitempty"`
	Columns []BoardColumn           `json:"columns"`
	Filters []BoardColumnPredicate  `json:"filters,omitempty"`
	Sort    []BoardViewSortRule     `json:"sort,omitempty"`
	Options BoardViewDisplayOptions `json:"options,omitempty"`
}

type BoardViewDisplayOptions struct {
	HideEmptyColumns bool                `json:"hide_empty_columns,omitempty"`
	SortPolicy       BoardViewSortPolicy `json:"sort_policy,omitempty"`
}

type BoardViewSortPolicy string

const (
	BoardViewSortDefault        BoardViewSortPolicy = ""
	BoardViewSortHumanAttention BoardViewSortPolicy = "human_attention"
)

type BoardViewSortKey string

const (
	BoardViewSortKeyHumanAttention BoardViewSortKey = "human_attention"
	BoardViewSortKeyPriority       BoardViewSortKey = "priority"
	BoardViewSortKeyUpdated        BoardViewSortKey = "updated"
	BoardViewSortKeySession        BoardViewSortKey = "session"
	BoardViewSortKeyGitDiff        BoardViewSortKey = "git_diff"
	BoardViewSortKeyIssueID        BoardViewSortKey = "issue_id"
)

type BoardViewSortDirection string

const (
	BoardViewSortAscending  BoardViewSortDirection = "asc"
	BoardViewSortDescending BoardViewSortDirection = "desc"
)

type BoardViewSortRule struct {
	Key       BoardViewSortKey       `json:"key"`
	Direction BoardViewSortDirection `json:"direction,omitempty"`
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

type BoardViewRecord struct {
	ProjectID string    `json:"project_id"`
	View      BoardView `json:"view"`
	BuiltIn   bool      `json:"built_in"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type BoardViewColumnSnapshot struct {
	Definition BoardColumn `json:"definition"`
	Tasks      []Task      `json:"tasks"`
}

// BoardViewProjection is the surface-neutral result of applying a view.
// Renderers choose geometry and interaction; placement and ordering live here.
type BoardViewProjection struct {
	View         BoardView                 `json:"view"`
	Groups       []BoardViewProjectedGroup `json:"groups"`
	Items        []BoardViewProjectedItem  `json:"items"`
	KnownTaskIDs []naming.IssueID          `json:"known_task_ids,omitempty"`
}

type BoardViewProjectedGroup struct {
	GroupID BoardColumnID    `json:"group_id"`
	TaskIDs []naming.IssueID `json:"task_ids"`
}

type BoardViewProjectedItem struct {
	Task               Task                   `json:"task"`
	GroupID            BoardColumnID          `json:"group_id"`
	Depth              int                    `json:"depth,omitempty"`
	OrchestrationState OrchestrationViewState `json:"orchestration_state,omitempty"`
}

// OrchestrationViewState exposes daemon-derived orchestration semantics to all
// view renderers without requiring them to recreate candidate policy.
type OrchestrationViewState string

const (
	OrchestrationViewReady        OrchestrationViewState = "ready"
	OrchestrationViewActive       OrchestrationViewState = "active"
	OrchestrationViewReview       OrchestrationViewState = "review"
	OrchestrationViewWaitingHuman OrchestrationViewState = "waiting_human"
	OrchestrationViewOwned        OrchestrationViewState = "owned"
	OrchestrationViewBacklog      OrchestrationViewState = "backlog"
)

func TaskOrchestrationViewState(task Task) OrchestrationViewState {
	facts := task.IssueFacts()
	switch {
	case task.Ownership != nil && strings.TrimSpace(task.Ownership.OwnerID) != "":
		return OrchestrationViewOwned
	case facts.WaitingHuman:
		return OrchestrationViewWaitingHuman
	case facts.ReviewReadyVisible || facts.ReviewState == IssueReviewRequested:
		return OrchestrationViewReview
	case facts.HasActiveSession || facts.LifecycleState == IssueWorkflowActive:
		return OrchestrationViewActive
	case facts.LifecycleState == IssueWorkflowBacklog:
		return OrchestrationViewBacklog
	default:
		return OrchestrationViewReady
	}
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

type persistedBoardViewDefinition struct {
	SchemaVersion int                     `json:"schema_version"`
	ID            BoardViewID             `json:"id"`
	Title         string                  `json:"title"`
	Name          string                  `json:"name,omitempty"`
	Layout        BoardViewLayout         `json:"layout,omitempty"`
	Columns       []BoardColumn           `json:"columns"`
	Filters       []BoardColumnPredicate  `json:"filters,omitempty"`
	Sort          []BoardViewSortRule     `json:"sort,omitempty"`
	Options       BoardViewDisplayOptions `json:"options,omitempty"`
}

func DecodeBoardViewDefinitionJSON(data []byte) (BoardView, error) {
	var persisted persistedBoardViewDefinition
	if err := json.Unmarshal(data, &persisted); err != nil {
		return BoardView{}, err
	}
	if persisted.SchemaVersion != 1 && persisted.SchemaVersion != BoardViewDefinitionSchemaVersion {
		return BoardView{}, fmt.Errorf("unsupported board view schema_version %d", persisted.SchemaVersion)
	}
	title := persisted.Title
	if strings.TrimSpace(title) == "" {
		title = persisted.Name
	}
	view := BoardView{
		ID:      persisted.ID,
		Title:   title,
		Layout:  persisted.Layout,
		Columns: persisted.Columns,
		Filters: persisted.Filters,
		Sort:    persisted.Sort,
		Options: persisted.Options,
	}
	view = view.Normalized()
	if err := view.Validate(); err != nil {
		return BoardView{}, err
	}
	return view, nil
}

func EncodeBoardViewDefinitionJSON(view BoardView) ([]byte, error) {
	view = view.Normalized()
	if err := view.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(persistedBoardViewDefinition{
		SchemaVersion: BoardViewDefinitionSchemaVersion,
		ID:            view.ID,
		Title:         view.Title,
		Layout:        view.Layout,
		Columns:       view.Columns,
		Filters:       view.Filters,
		Sort:          view.Sort,
		Options:       view.Options,
	})
}

func BuiltInBoardViewSet() BoardViewSet {
	return BoardViewSet{
		DefaultViewID: BoardViewDefaultID,
		Views: []BoardView{
			DefaultBoardView(),
			PlanningBoardView(),
			OrchestrationBoardView(),
			CloseoutBoardView(),
			GridBoardView(),
			TreeBoardView(),
		},
	}
}

func GridBoardView() BoardView {
	view := DefaultBoardView()
	view.ID, view.Title, view.Layout = BoardViewGridID, "Grid", BoardViewLayoutHorizontalGrid
	return view
}

func TreeBoardView() BoardView {
	view := DefaultBoardView()
	view.ID, view.Title, view.Layout = BoardViewTreeID, "Tree", BoardViewLayoutTreeList
	view.Sort = DefaultBoardViewSortRules()
	return view
}

func BuiltInBoardViews() []BoardView {
	views := BuiltInBoardViewSet().Views
	return append([]BoardView(nil), views...)
}

func DefaultBoardView() BoardView {
	return BoardView{
		ID:      BoardViewDefaultID,
		Title:   "Default",
		Layout:  BoardViewLayoutColumnBoard,
		Columns: defaultBoardViewColumns(),
		Sort:    DefaultBoardViewSortRules(),
		Options: BoardViewDisplayOptions{SortPolicy: BoardViewSortHumanAttention},
	}
}

func DefaultBoardViewSortRules() []BoardViewSortRule {
	return []BoardViewSortRule{
		{Key: BoardViewSortKeyHumanAttention, Direction: BoardViewSortDescending},
		{Key: BoardViewSortKeyPriority, Direction: BoardViewSortAscending},
		{Key: BoardViewSortKeyUpdated, Direction: BoardViewSortDescending},
		{Key: BoardViewSortKeySession, Direction: BoardViewSortDescending},
		{Key: BoardViewSortKeyGitDiff, Direction: BoardViewSortDescending},
		{Key: BoardViewSortKeyIssueID, Direction: BoardViewSortAscending},
	}
}

// CurrentBoardView is the source-compatible name for the canonical Default view.
func CurrentBoardView() BoardView {
	return DefaultBoardView()
}

func defaultBoardViewColumns() []BoardColumn {
	return []BoardColumn{
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
	}
}

func PlanningBoardView() BoardView {
	view := DefaultBoardView()
	return BoardView{
		ID:      BoardViewPlanningID,
		Title:   "Planning",
		Options: view.Options,
		Columns: []BoardColumn{
			{
				ID:    BoardColumnBacklog,
				Title: "Backlog",
				Predicates: []BoardColumnPredicate{{
					Kind:      BoardPredicateLifecycle,
					Lifecycle: []IssueWorkflow{IssueWorkflowBacklog},
				}},
			},
			view.Columns[0],
			view.Columns[1],
			view.Columns[2],
		},
	}
}

// ActivityBoardView is the source-compatible name for Orchestration.
func ActivityBoardView() BoardView {
	return OrchestrationBoardView()
}

func OrchestrationBoardView() BoardView {
	view := DefaultBoardView()
	view.ID = BoardViewOrchestrationID
	view.Title = "Orchestration"
	view.Columns = []BoardColumn{
		{
			ID:    BoardColumnReviewReady,
			Title: "Human Review",
			Predicates: []BoardColumnPredicate{{
				Kind: BoardPredicateReviewReady,
			}},
		},
		{
			ID:    BoardColumnWaitingAI,
			Title: "AI Review",
			Predicates: []BoardColumnPredicate{{
				Kind: BoardPredicateWaitingAIDelegated,
			}},
		},
		{
			ID:         BoardColumnActive,
			Title:      "In Progress",
			Predicates: view.Columns[1].Predicates,
		},
	}
	return view
}

func CloseoutBoardView() BoardView {
	view := DefaultBoardView()
	return BoardView{
		ID:      BoardViewCloseoutID,
		Title:   "Closeout",
		Options: view.Options,
		Columns: []BoardColumn{
			view.Columns[2],
			view.Columns[3],
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

func GroupTasksByBoardView(view BoardView, tasks []Task) ([]BoardViewColumnSnapshot, error) {
	projection, err := ProjectTasksByBoardView(view, tasks)
	if err != nil {
		return nil, err
	}
	return projection.ColumnSnapshots(), nil
}

func (p BoardViewProjection) ColumnSnapshots() []BoardViewColumnSnapshot {
	items := make(map[string]Task, len(p.Items))
	for _, item := range p.Items {
		items[item.Task.ID.String()] = item.Task
	}
	columns := make([]BoardViewColumnSnapshot, 0, len(p.Groups))
	definitions := make(map[BoardColumnID]BoardColumn, len(p.View.Columns))
	for _, definition := range p.View.Columns {
		definitions[definition.ID] = definition
	}
	for _, group := range p.Groups {
		column := BoardViewColumnSnapshot{Definition: definitions[group.GroupID]}
		for _, taskID := range group.TaskIDs {
			if task, ok := items[taskID.String()]; ok {
				column.Tasks = append(column.Tasks, task)
			}
		}
		columns = append(columns, column)
	}
	return columns
}

func (p BoardViewProjection) OrderedTasks() []Task {
	result := make([]Task, 0, len(p.Items))
	for _, item := range p.Items {
		result = append(result, item.Task)
	}
	return result
}

func ProjectTasksByBoardView(view BoardView, tasks []Task) (BoardViewProjection, error) {
	view = view.Normalized()
	if err := view.Validate(); err != nil {
		return BoardViewProjection{}, err
	}
	columns := make([]BoardViewColumnSnapshot, 0, len(view.Columns))
	for _, column := range view.Columns {
		columns = append(columns, BoardViewColumnSnapshot{Definition: column})
	}
	for _, task := range tasks {
		if !view.MatchesFilters(task) {
			continue
		}
		placement := view.placeTaskNormalized(task)
		if placement.Matched && placement.ColumnIndex >= 0 && placement.ColumnIndex < len(columns) {
			columns[placement.ColumnIndex].Tasks = append(columns[placement.ColumnIndex].Tasks, task)
		}
	}
	for i := range columns {
		columns[i].Tasks = ApplyBoardViewSortRulesInPlace(view.effectiveSortRules(), columns[i].Tasks)
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
	ordered := make([]Task, 0, len(tasks))
	groupByTask := make(map[string]BoardColumnID, len(tasks))
	for _, column := range columns {
		for _, task := range column.Tasks {
			ordered = append(ordered, task)
			groupByTask[task.ID.String()] = column.Definition.ID
		}
	}
	if view.Layout == BoardViewLayoutTreeList {
		ordered = ApplyBoardViewSortRulesInPlace(view.effectiveSortRules(), ordered)
		ordered = boardViewTreeOrder(ordered)
	}
	depths := boardViewTreeDepths(ordered)
	groups := make([]BoardViewProjectedGroup, 0, len(columns))
	for _, column := range columns {
		group := BoardViewProjectedGroup{GroupID: column.Definition.ID}
		for _, task := range column.Tasks {
			group.TaskIDs = append(group.TaskIDs, task.ID)
		}
		groups = append(groups, group)
	}
	items := make([]BoardViewProjectedItem, 0, len(ordered))
	for _, task := range ordered {
		items = append(items, BoardViewProjectedItem{
			Task:               task,
			GroupID:            groupByTask[task.ID.String()],
			Depth:              depths[task.ID.String()],
			OrchestrationState: TaskOrchestrationViewState(task),
		})
	}
	knownTaskIDs := make([]naming.IssueID, 0, len(tasks))
	for _, task := range tasks {
		knownTaskIDs = append(knownTaskIDs, task.ID)
	}
	return BoardViewProjection{View: view, Groups: groups, Items: items, KnownTaskIDs: knownTaskIDs}, nil
}

func boardViewTreeDepths(tasks []Task) map[string]int {
	byID := make(map[string]struct{}, len(tasks))
	parents := make(map[string]string, len(tasks))
	for _, task := range tasks {
		byID[task.ID.String()] = struct{}{}
		if parentID := boardViewTaskParentID(task); parentID != "" {
			parents[task.ID.String()] = parentID
		}
	}
	depths := make(map[string]int, len(tasks))
	for _, task := range tasks {
		seen := map[string]bool{task.ID.String(): true}
		for parentID := parents[task.ID.String()]; parentID != "" && !seen[parentID]; parentID = parents[parentID] {
			if _, included := byID[parentID]; !included {
				break
			}
			seen[parentID] = true
			depths[task.ID.String()]++
		}
	}
	return depths
}

func boardViewTaskParentID(task Task) string {
	if task.ParentID != nil {
		return task.ParentID.String()
	}
	for _, dependency := range task.Dependencies {
		if dependency.Type == DependencyParentChild {
			return dependency.ID.String()
		}
	}
	return ""
}

func boardViewTreeOrder(tasks []Task) []Task {
	byID := make(map[string]Task, len(tasks))
	children := make(map[string][]string, len(tasks))
	roots := make([]string, 0, len(tasks))
	for _, task := range tasks {
		byID[task.ID.String()] = task
	}
	for _, task := range tasks {
		id := task.ID.String()
		parentID := boardViewTaskParentID(task)
		if parentID == "" || parentID == id {
			roots = append(roots, id)
		} else if _, ok := byID[parentID]; ok {
			children[parentID] = append(children[parentID], id)
		} else {
			roots = append(roots, id)
		}
	}
	ordered := make([]Task, 0, len(tasks))
	seen := make(map[string]bool, len(tasks))
	var visit func(string)
	visit = func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		ordered = append(ordered, byID[id])
		for _, childID := range children[id] {
			visit(childID)
		}
	}
	for _, rootID := range roots {
		visit(rootID)
	}
	for _, task := range tasks {
		visit(task.ID.String())
	}
	return ordered
}

func (v BoardView) MatchesFilters(task Task) bool {
	for _, filter := range v.Filters {
		matched, _ := filter.MatchTask(task)
		if !matched {
			return false
		}
	}
	return true
}

func (v BoardView) effectiveSortRules() []BoardViewSortRule {
	if len(v.Sort) > 0 {
		return v.Sort
	}
	if v.Options.SortPolicy == BoardViewSortHumanAttention {
		return []BoardViewSortRule{{Key: BoardViewSortKeyHumanAttention, Direction: BoardViewSortDescending}}
	}
	return nil
}

func ApplyBoardViewSortRulesInPlace(rules []BoardViewSortRule, tasks []Task) []Task {
	if len(rules) == 0 || len(tasks) < 2 {
		return tasks
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		for _, rule := range rules {
			comparison := CompareBoardViewTasks(rule.Key, tasks[i], tasks[j])
			if comparison == 0 {
				continue
			}
			if rule.Direction == BoardViewSortDescending {
				return comparison > 0
			}
			return comparison < 0
		}
		return false
	})
	return tasks
}

// CompareBoardViewTasks compares two tasks using a validated view sort key.
// Cross-project evaluators use this to preserve the same domain ordering while
// retaining project-scoped identities outside Task.
func CompareBoardViewTasks(key BoardViewSortKey, left, right Task) int {
	compareInt := func(a, b int) int {
		if a < b {
			return -1
		}
		if a > b {
			return 1
		}
		return 0
	}
	switch key {
	case BoardViewSortKeyHumanAttention:
		return compareInt(int(HumanAttentionRank(left)), int(HumanAttentionRank(right)))
	case BoardViewSortKeyPriority:
		return compareInt(int(left.Priority), int(right.Priority))
	case BoardViewSortKeyUpdated:
		return left.UpdatedAt.Compare(right.UpdatedAt)
	case BoardViewSortKeySession:
		return compareInt(sessionPriority(left), sessionPriority(right))
	case BoardViewSortKeyGitDiff:
		return compareInt(gitDiffTotal(left), gitDiffTotal(right))
	case BoardViewSortKeyIssueID:
		return strings.Compare(left.ID.String(), right.ID.String())
	default:
		return 0
	}
}

// ApplyBoardViewSortPolicyInPlace applies the view-owned ordering prefix while
// preserving the caller's existing order as the stable tie-breaker.
func ApplyBoardViewSortPolicyInPlace(policy BoardViewSortPolicy, tasks []Task) []Task {
	if policy != BoardViewSortHumanAttention || len(tasks) < 2 {
		return tasks
	}
	sort.SliceStable(tasks, func(left, right int) bool {
		return HumanAttentionRank(tasks[left]) > HumanAttentionRank(tasks[right])
	})
	return tasks
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
	id = BoardViewID(NormalizeBoardViewID(string(id)))
	for _, view := range s.Views {
		if view.ID == id {
			return view, true
		}
	}
	return BoardView{}, false
}

func (v BoardView) Normalized() BoardView {
	v.ID = BoardViewID(NormalizeBoardViewID(string(v.ID)))
	v.Title = strings.TrimSpace(v.Title)
	if v.Layout == "" {
		v.Layout = BoardViewLayoutColumnBoard
	}
	if len(v.Sort) == 0 && v.Options.SortPolicy == BoardViewSortHumanAttention {
		v.Sort = []BoardViewSortRule{{Key: BoardViewSortKeyHumanAttention, Direction: BoardViewSortDescending}}
	}
	for i := range v.Columns {
		v.Columns[i] = v.Columns[i].Normalized()
	}
	return v
}

func (v BoardView) Validate() error {
	v = v.Normalized()
	if strings.TrimSpace(string(v.ID)) == "" {
		return fmt.Errorf("board view id is required")
	}
	if strings.TrimSpace(v.Title) == "" {
		return fmt.Errorf("board view %q title is required", v.ID)
	}
	if len(v.Columns) == 0 {
		return fmt.Errorf("board view %q must define at least one column", v.ID)
	}
	switch v.Layout {
	case BoardViewLayoutColumnBoard, BoardViewLayoutTreeList, BoardViewLayoutHorizontalGrid:
	default:
		return fmt.Errorf("board view %q has unsupported layout %q", v.ID, v.Layout)
	}
	switch v.Options.SortPolicy {
	case BoardViewSortDefault, BoardViewSortHumanAttention:
	default:
		return fmt.Errorf("board view %q has unsupported sort policy %q", v.ID, v.Options.SortPolicy)
	}
	for i, rule := range v.Sort {
		switch rule.Key {
		case BoardViewSortKeyHumanAttention, BoardViewSortKeyPriority, BoardViewSortKeyUpdated, BoardViewSortKeySession, BoardViewSortKeyGitDiff, BoardViewSortKeyIssueID:
		default:
			return fmt.Errorf("board view %q sort rule %d has unsupported key %q", v.ID, i, rule.Key)
		}
		switch rule.Direction {
		case "", BoardViewSortAscending, BoardViewSortDescending:
		default:
			return fmt.Errorf("board view %q sort rule %d has unsupported direction %q", v.ID, i, rule.Direction)
		}
	}
	for i, filter := range v.Filters {
		if err := filter.Validate(); err != nil {
			return fmt.Errorf("filter %d: %w", i, err)
		}
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

func (c BoardColumn) Normalized() BoardColumn {
	c.ID = BoardColumnID(normalizeBoardGroupID(string(c.ID)))
	c.Title = strings.TrimSpace(c.Title)
	for i := range c.Predicates {
		c.Predicates[i] = c.Predicates[i].Normalized()
	}
	return c
}

func normalizeBoardGroupID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.ReplaceAll(value, " ", "-")
}

func (c BoardColumn) Validate() error {
	c = c.Normalized()
	if strings.TrimSpace(string(c.ID)) == "" {
		return fmt.Errorf("board column id is required")
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
	out := BoardColumnPredicate(decoded).Normalized()
	if err := out.Validate(); err != nil {
		return err
	}
	*p = out
	return nil
}

func (p BoardColumnPredicate) Normalized() BoardColumnPredicate {
	p.Kind = BoardColumnPredicateKind(strings.TrimSpace(string(p.Kind)))
	p.Lifecycle = normalizeBoardPredicateValues(p.Lifecycle)
	p.DisplayPhases = normalizeBoardPredicateValues(p.DisplayPhases)
	p.ClosedOutcomes = normalizeBoardPredicateValues(p.ClosedOutcomes)
	return p
}

func (p BoardColumnPredicate) Validate() error {
	p = p.Normalized()
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
	v = v.Normalized()
	if err := v.Validate(); err != nil {
		return BoardPlacement{}, err
	}
	return v.placeTaskNormalized(task), nil
}

func (v BoardView) placeTaskNormalized(task Task) BoardPlacement {
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
		}
	}
	return BoardPlacement{
		Matched:     false,
		ViewID:      v.ID,
		ColumnIndex: -1,
		MatchReason: "no board column predicates matched",
	}
}

func (c BoardColumn) MatchTask(task Task) (bool, []string) {
	c = c.Normalized()
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
	p = p.Normalized()
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
		if TaskReviewReady(task) {
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
		facts := task.IssueFacts()
		if facts.WaitingHuman {
			if facts.WaitingHumanSource != "" {
				return true, "waiting_human=true source=" + string(facts.WaitingHumanSource)
			}
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

func TaskReviewReady(task Task) bool {
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
		predicate = predicate.Normalized()
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

func NormalizeBoardViewID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "-")
	switch value {
	case string(BoardViewCurrentID):
		return string(BoardViewDefaultID)
	case string(BoardViewActivityID):
		return string(BoardViewOrchestrationID)
	}
	return value
}

// BoardViewIDFromLegacyUIMode maps retired surface-local modes onto saved
// views. It is a one-way persistence migration, not a rendering mode adapter.
func BoardViewIDFromLegacyUIMode(value string) (string, bool) {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "board":
		return string(BoardViewDefaultID), true
	case "compact":
		return string(BoardViewTreeID), true
	case "overview", "orchestration":
		return string(BoardViewOrchestrationID), true
	default:
		return "", false
	}
}

func normalizeBoardPredicateValues[T ~string](values []T) []T {
	out := values[:0]
	seen := map[T]struct{}{}
	for _, value := range values {
		value = T(strings.TrimSpace(string(value)))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	slices.Sort(out)
	return out
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
