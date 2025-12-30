package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ProjectsRegistry holds the list of known projects
type ProjectsRegistry struct {
	Projects       []Project `json:"projects"`
	DefaultProject string    `json:"defaultProject"`
}

// Project represents a registered project
type Project struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

var (
	// ErrProjectNotFound is returned when a project doesn't exist in the registry
	ErrProjectNotFound = errors.New("project not found")
	// ErrDuplicateProject is returned when trying to add a project that already exists
	ErrDuplicateProject = errors.New("project already exists")
	// ErrEmptyName is returned when the project name is empty
	ErrEmptyName = errors.New("project name cannot be empty")
	// ErrEmptyPath is returned when the project path is empty
	ErrEmptyPath = errors.New("project path cannot be empty")
	// ErrNotGitRepo is returned when the path is not a git repository
	ErrNotGitRepo = errors.New("path is not a git repository")
)

// LoadProjectsRegistry loads the projects registry from disk
// Returns an empty registry if the file doesn't exist
func LoadProjectsRegistry() (*ProjectsRegistry, error) {
	path, err := registryPath()
	if err != nil {
		return nil, err
	}

	// Return empty registry if file doesn't exist
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return &ProjectsRegistry{
			Projects:       []Project{},
			DefaultProject: "",
		}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var registry ProjectsRegistry
	if err := json.Unmarshal(data, &registry); err != nil {
		return nil, err
	}

	return &registry, nil
}

// SaveProjectsRegistry saves the projects registry to disk
func SaveProjectsRegistry(reg *ProjectsRegistry) error {
	path, err := registryPath()
	if err != nil {
		return err
	}

	// Ensure config directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// Add adds a new project to the registry
func (r *ProjectsRegistry) Add(name, path string) error {
	if name == "" {
		return ErrEmptyName
	}
	if path == "" {
		return ErrEmptyPath
	}

	// Check if project already exists
	for _, p := range r.Projects {
		if p.Name == name {
			return ErrDuplicateProject
		}
	}

	// Validate that path is a git repository
	if !isGitRepo(path) {
		return ErrNotGitRepo
	}

	// Add project
	r.Projects = append(r.Projects, Project{
		Name: name,
		Path: path,
	})

	// Set as default if it's the first project
	if len(r.Projects) == 1 {
		r.DefaultProject = name
	}

	return nil
}

// Remove removes a project from the registry
func (r *ProjectsRegistry) Remove(name string) error {
	if name == "" {
		return ErrEmptyName
	}

	// Find and remove project
	found := false
	for i, p := range r.Projects {
		if p.Name == name {
			r.Projects = append(r.Projects[:i], r.Projects[i+1:]...)
			found = true
			break
		}
	}

	if !found {
		return ErrProjectNotFound
	}

	// Clear default if it was the removed project
	if r.DefaultProject == name {
		r.DefaultProject = ""
		// Set new default to first project if any remain
		if len(r.Projects) > 0 {
			r.DefaultProject = r.Projects[0].Name
		}
	}

	return nil
}

// SetDefault sets the default project
func (r *ProjectsRegistry) SetDefault(name string) error {
	if name == "" {
		return ErrEmptyName
	}

	// Verify project exists
	found := false
	for _, p := range r.Projects {
		if p.Name == name {
			found = true
			break
		}
	}

	if !found {
		return ErrProjectNotFound
	}

	r.DefaultProject = name
	return nil
}

// Get retrieves a project by name
func (r *ProjectsRegistry) Get(name string) (*Project, error) {
	for _, p := range r.Projects {
		if p.Name == name {
			return &p, nil
		}
	}
	return nil, ErrProjectNotFound
}

// GetDefault returns the default project, or nil if none is set
func (r *ProjectsRegistry) GetDefault() *Project {
	if r.DefaultProject == "" {
		return nil
	}
	for _, p := range r.Projects {
		if p.Name == r.DefaultProject {
			return &p
		}
	}
	return nil
}

// DetectProjectFromCwd attempts to detect a project from the current directory
// It walks up the directory tree looking for a .git directory
func DetectProjectFromCwd() (*Project, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	// Walk up directory tree looking for .git
	path := cwd
	for {
		if isGitRepo(path) {
			// Use the directory name as the project name
			name := filepath.Base(path)
			return &Project{
				Name: name,
				Path: path,
			}, nil
		}

		// Move up one directory
		parent := filepath.Dir(path)
		if parent == path {
			// Reached root without finding .git
			return nil, ErrNotGitRepo
		}
		path = parent
	}
}

// registryPath is a variable holding the function that returns the path to the projects registry file
// This allows it to be overridden in tests
var registryPath = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "azedarach", "projects.json"), nil
}

// isGitRepo checks if a path is a git repository
func isGitRepo(path string) bool {
	gitPath := filepath.Join(path, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return false
	}
	// .git can be either a directory or a file (for worktrees)
	return info.IsDir() || !info.IsDir()
}

// FindByPath finds a project by its path
func (r *ProjectsRegistry) FindByPath(path string) *Project {
	// Normalize path by cleaning it
	cleanPath := filepath.Clean(path)

	for _, p := range r.Projects {
		cleanProjectPath := filepath.Clean(p.Path)
		if cleanProjectPath == cleanPath || strings.HasPrefix(cleanPath, cleanProjectPath+string(filepath.Separator)) {
			return &p
		}
	}
	return nil
}
