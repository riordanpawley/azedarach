package projects

import "fmt"

type Project struct {
	ID   string
	Name string
	Root string
}

type ContextRestore struct {
	ProjectID string
	Lane      string
	IssueID   string
}

type Registry struct {
	projects map[string]Project
	order    []string
	restore  ContextRestore
}

func NewRegistry() *Registry {
	return &Registry{
		projects: map[string]Project{},
		order:    make([]string, 0, 8),
	}
}

func (r *Registry) Register(project Project) error {
	if project.ID == "" {
		return fmt.Errorf("project id is required")
	}
	if _, exists := r.projects[project.ID]; exists {
		return fmt.Errorf("project already registered: %s", project.ID)
	}

	r.projects[project.ID] = project
	r.order = append(r.order, project.ID)
	return nil
}

func (r *Registry) List() []Project {
	out := make([]Project, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.projects[id])
	}
	return out
}

func (r *Registry) Select(projectID string) error {
	if _, ok := r.projects[projectID]; !ok {
		return fmt.Errorf("project not found: %s", projectID)
	}
	r.restore.ProjectID = projectID
	return nil
}

func (r *Registry) Current() (Project, bool) {
	if r.restore.ProjectID == "" {
		return Project{}, false
	}

	project, ok := r.projects[r.restore.ProjectID]
	if !ok {
		return Project{}, false
	}

	return project, true
}

func (r *Registry) SetContextRestore(metadata ContextRestore) error {
	if metadata.ProjectID != "" {
		if _, ok := r.projects[metadata.ProjectID]; !ok {
			return fmt.Errorf("restore project not found: %s", metadata.ProjectID)
		}
	}
	r.restore = metadata
	return nil
}

func (r *Registry) ContextRestore() ContextRestore {
	return r.restore
}
