package app

type tuiScopeKind uint8

const (
	tuiScopeProject tuiScopeKind = iota
	tuiScopeGlobal
)

type tuiScope struct {
	kind tuiScopeKind
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
