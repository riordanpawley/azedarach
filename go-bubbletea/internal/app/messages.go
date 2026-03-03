package app

import "github.com/riordanpawley/azedarach/internal/domain"

type ChangeModeMsg struct {
	Mode domain.Mode
}

type ToggleViewMsg struct{}

type MoveCursorMsg struct {
	ColumnDelta int
	RowDelta    int
}

type SetSearchQueryMsg struct {
	Query string
}

type SetFilterStatusMsg struct {
	Status *domain.Status
}

type SetSortFieldMsg struct {
	Field domain.SortField
}

type ToggleSelectCurrentMsg struct{}

type FreezeSelectionMsg struct{}

type BuildGotoLabelsMsg struct{}

type JumpToLabelMsg struct {
	Label string
}

type StartOperationMsg struct{}

type SetOperationStateMsg struct {
	State domain.OperationState
}

type SetStatusMsg struct {
	Status domain.Status
}
