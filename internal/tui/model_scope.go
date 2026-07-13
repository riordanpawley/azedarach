package app

type tuiScopeKind uint8

const (
	tuiScopeProject tuiScopeKind = iota
	tuiScopeGlobal
)

type tuiScope struct {
	kind tuiScopeKind
}

type boardViewCommandScope struct {
	global     bool
	projectID  string
	generation uint64
}

func projectTUIScope() tuiScope { return tuiScope{kind: tuiScopeProject} }

func globalTUIScope() tuiScope { return tuiScope{kind: tuiScopeGlobal} }

func (s tuiScope) IsGlobal() bool { return s.kind == tuiScopeGlobal }

func (s tuiScope) Label(projectName string) string {
	if s.IsGlobal() {
		return "Global"
	}
	if projectName == "" {
		return "Project"
	}
	return projectName
}

func (m Model) currentBoardViewCommandScope() boardViewCommandScope {
	scope := boardViewCommandScope{
		global:     m.scope.IsGlobal(),
		generation: m.boardViewScopeGeneration,
	}
	if !scope.global {
		scope.projectID = m.daemonProjectID()
	}
	return scope
}

func (m *Model) beginBoardViewScopeTransition() {
	m.boardViewScopeGeneration++
}
