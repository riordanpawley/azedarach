package domain

type Mode string

const (
	ModeNormal Mode = "NOR"
	ModeAction Mode = "ACT"
	ModeGoto   Mode = "GTO"
	ModeSelect Mode = "SEL"
	ModeSearch Mode = "SRC"
	ModeFilter Mode = "FLT"
	ModeSort   Mode = "SRT"
)

func (m Mode) Valid() bool {
	switch m {
	case ModeNormal, ModeAction, ModeGoto, ModeSelect, ModeSearch, ModeFilter, ModeSort:
		return true
	default:
		return false
	}
}

type ViewMode string

const (
	ViewKanban ViewMode = "kanban"
	ViewList   ViewMode = "list"
)

func (v ViewMode) Valid() bool {
	switch v {
	case ViewKanban, ViewList:
		return true
	default:
		return false
	}
}

func (s Status) Valid() bool {
	switch s {
	case StatusOpen, StatusInProgress, StatusBlocked, StatusDone:
		return true
	default:
		return false
	}
}

type Relation string

const (
	RelationBlocks         Relation = "blocks"
	RelationDependsOn      Relation = "depends_on"
	RelationDiscoveredFrom Relation = "discovered_from"
)

func (r Relation) Valid() bool {
	switch r {
	case RelationBlocks, RelationDependsOn, RelationDiscoveredFrom:
		return true
	default:
		return false
	}
}

type OperationState string

const (
	OperationQueued     OperationState = "queued"
	OperationRunning    OperationState = "running"
	OperationSucceeded  OperationState = "succeeded"
	OperationFailed     OperationState = "failed"
	OperationCancelled  OperationState = "cancelled"
	OperationRolledBack OperationState = "rolled_back"
)

func (s OperationState) Valid() bool {
	switch s {
	case OperationQueued, OperationRunning, OperationSucceeded, OperationFailed, OperationCancelled, OperationRolledBack:
		return true
	default:
		return false
	}
}

func (s SortField) Valid() bool {
	switch s {
	case SortBySession, SortByPriority, SortByUpdated, SortByTitle, SortByID:
		return true
	default:
		return false
	}
}
