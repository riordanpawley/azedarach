package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProjectsRegistry_Add(t *testing.T) {
	tests := []struct {
		name    string
		initial []Project
		addName string
		addPath string
		wantErr error
		wantLen int
	}{
		{
			name:    "add first project",
			initial: []Project{},
			addName: "test",
			addPath: createTempGitRepo(t),
			wantErr: nil,
			wantLen: 1,
		},
		{
			name: "add second project",
			initial: []Project{
				{Name: "existing", Path: "/tmp/existing"},
			},
			addName: "test",
			addPath: createTempGitRepo(t),
			wantErr: nil,
			wantLen: 2,
		},
		{
			name: "duplicate name",
			initial: []Project{
				{Name: "test", Path: "/tmp/test"},
			},
			addName: "test",
			addPath: createTempGitRepo(t),
			wantErr: ErrDuplicateProject,
			wantLen: 1,
		},
		{
			name:    "empty name",
			initial: []Project{},
			addName: "",
			addPath: createTempGitRepo(t),
			wantErr: ErrEmptyName,
			wantLen: 0,
		},
		{
			name:    "empty path",
			initial: []Project{},
			addName: "test",
			addPath: "",
			wantErr: ErrEmptyPath,
			wantLen: 0,
		},
		{
			name:    "not a git repo",
			initial: []Project{},
			addName: "test",
			addPath: t.TempDir(),
			wantErr: ErrNotGitRepo,
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &ProjectsRegistry{
				Projects: tt.initial,
			}

			err := reg.Add(tt.addName, tt.addPath)

			if err != tt.wantErr {
				t.Errorf("Add() error = %v, wantErr %v", err, tt.wantErr)
			}

			if len(reg.Projects) != tt.wantLen {
				t.Errorf("Add() projects length = %d, want %d", len(reg.Projects), tt.wantLen)
			}

			// Check default is set for first project
			if tt.wantLen == 1 && tt.wantErr == nil {
				if reg.DefaultProject != tt.addName {
					t.Errorf("Add() default project = %s, want %s", reg.DefaultProject, tt.addName)
				}
			}
		})
	}
}

func TestProjectsRegistry_Remove(t *testing.T) {
	tests := []struct {
		name           string
		initial        []Project
		defaultProject string
		removeName     string
		wantErr        error
		wantLen        int
		wantDefault    string
	}{
		{
			name: "remove existing project",
			initial: []Project{
				{Name: "test", Path: "/tmp/test"},
				{Name: "other", Path: "/tmp/other"},
			},
			defaultProject: "other",
			removeName:     "test",
			wantErr:        nil,
			wantLen:        1,
			wantDefault:    "other",
		},
		{
			name: "remove default project",
			initial: []Project{
				{Name: "test", Path: "/tmp/test"},
				{Name: "other", Path: "/tmp/other"},
			},
			defaultProject: "test",
			removeName:     "test",
			wantErr:        nil,
			wantLen:        1,
			wantDefault:    "other",
		},
		{
			name: "remove last project",
			initial: []Project{
				{Name: "test", Path: "/tmp/test"},
			},
			defaultProject: "test",
			removeName:     "test",
			wantErr:        nil,
			wantLen:        0,
			wantDefault:    "",
		},
		{
			name: "remove non-existent project",
			initial: []Project{
				{Name: "test", Path: "/tmp/test"},
			},
			defaultProject: "test",
			removeName:     "missing",
			wantErr:        ErrProjectNotFound,
			wantLen:        1,
			wantDefault:    "test",
		},
		{
			name:           "remove empty name",
			initial:        []Project{{Name: "test", Path: "/tmp/test"}},
			defaultProject: "test",
			removeName:     "",
			wantErr:        ErrEmptyName,
			wantLen:        1,
			wantDefault:    "test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &ProjectsRegistry{
				Projects:       tt.initial,
				DefaultProject: tt.defaultProject,
			}

			err := reg.Remove(tt.removeName)

			if err != tt.wantErr {
				t.Errorf("Remove() error = %v, wantErr %v", err, tt.wantErr)
			}

			if len(reg.Projects) != tt.wantLen {
				t.Errorf("Remove() projects length = %d, want %d", len(reg.Projects), tt.wantLen)
			}

			if reg.DefaultProject != tt.wantDefault {
				t.Errorf("Remove() default project = %s, want %s", reg.DefaultProject, tt.wantDefault)
			}
		})
	}
}

func TestProjectsRegistry_SetDefault(t *testing.T) {
	tests := []struct {
		name        string
		initial     []Project
		setName     string
		wantErr     error
		wantDefault string
	}{
		{
			name: "set existing project",
			initial: []Project{
				{Name: "test", Path: "/tmp/test"},
				{Name: "other", Path: "/tmp/other"},
			},
			setName:     "other",
			wantErr:     nil,
			wantDefault: "other",
		},
		{
			name: "set non-existent project",
			initial: []Project{
				{Name: "test", Path: "/tmp/test"},
			},
			setName:     "missing",
			wantErr:     ErrProjectNotFound,
			wantDefault: "",
		},
		{
			name: "set empty name",
			initial: []Project{
				{Name: "test", Path: "/tmp/test"},
			},
			setName:     "",
			wantErr:     ErrEmptyName,
			wantDefault: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &ProjectsRegistry{
				Projects: tt.initial,
			}

			err := reg.SetDefault(tt.setName)

			if err != tt.wantErr {
				t.Errorf("SetDefault() error = %v, wantErr %v", err, tt.wantErr)
			}

			if err == nil && reg.DefaultProject != tt.wantDefault {
				t.Errorf("SetDefault() default project = %s, want %s", reg.DefaultProject, tt.wantDefault)
			}
		})
	}
}

func TestProjectsRegistry_Get(t *testing.T) {
	projects := []Project{
		{Name: "test", Path: "/tmp/test"},
		{Name: "other", Path: "/tmp/other"},
	}

	tests := []struct {
		name     string
		getName  string
		wantErr  error
		wantPath string
	}{
		{
			name:     "get existing project",
			getName:  "test",
			wantErr:  nil,
			wantPath: "/tmp/test",
		},
		{
			name:    "get non-existent project",
			getName: "missing",
			wantErr: ErrProjectNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &ProjectsRegistry{
				Projects: projects,
			}

			project, err := reg.Get(tt.getName)

			if err != tt.wantErr {
				t.Errorf("Get() error = %v, wantErr %v", err, tt.wantErr)
			}

			if err == nil && project.Path != tt.wantPath {
				t.Errorf("Get() path = %s, want %s", project.Path, tt.wantPath)
			}
		})
	}
}

func TestProjectsRegistry_GetDefault(t *testing.T) {
	tests := []struct {
		name           string
		projects       []Project
		defaultProject string
		wantPath       string
	}{
		{
			name: "get existing default",
			projects: []Project{
				{Name: "test", Path: "/tmp/test"},
				{Name: "other", Path: "/tmp/other"},
			},
			defaultProject: "test",
			wantPath:       "/tmp/test",
		},
		{
			name: "no default set",
			projects: []Project{
				{Name: "test", Path: "/tmp/test"},
			},
			defaultProject: "",
			wantPath:       "",
		},
		{
			name: "default not in projects",
			projects: []Project{
				{Name: "test", Path: "/tmp/test"},
			},
			defaultProject: "missing",
			wantPath:       "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &ProjectsRegistry{
				Projects:       tt.projects,
				DefaultProject: tt.defaultProject,
			}

			project := reg.GetDefault()

			if tt.wantPath == "" {
				if project != nil {
					t.Errorf("GetDefault() = %v, want nil", project)
				}
			} else {
				if project == nil {
					t.Errorf("GetDefault() = nil, want project")
				} else if project.Path != tt.wantPath {
					t.Errorf("GetDefault() path = %s, want %s", project.Path, tt.wantPath)
				}
			}
		})
	}
}

func TestProjectsRegistry_FindByPath(t *testing.T) {
	projects := []Project{
		{Name: "test", Path: "/home/user/test"},
		{Name: "other", Path: "/home/user/other"},
	}

	tests := []struct {
		name     string
		findPath string
		wantName string
	}{
		{
			name:     "exact match",
			findPath: "/home/user/test",
			wantName: "test",
		},
		{
			name:     "subdirectory match",
			findPath: "/home/user/test/subdir",
			wantName: "test",
		},
		{
			name:     "no match",
			findPath: "/home/user/missing",
			wantName: "",
		},
		{
			name:     "partial match should not match",
			findPath: "/home/user/test2",
			wantName: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &ProjectsRegistry{
				Projects: projects,
			}

			project := reg.FindByPath(tt.findPath)

			if tt.wantName == "" {
				if project != nil {
					t.Errorf("FindByPath() = %v, want nil", project)
				}
			} else {
				if project == nil {
					t.Errorf("FindByPath() = nil, want project")
				} else if project.Name != tt.wantName {
					t.Errorf("FindByPath() name = %s, want %s", project.Name, tt.wantName)
				}
			}
		})
	}
}

func TestLoadSaveProjectsRegistry(t *testing.T) {
	// Create a temporary config directory
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "projects.json")

	// Override registryPath for testing
	originalRegistryPath := registryPath
	registryPath = func() (string, error) {
		return configPath, nil
	}
	defer func() { registryPath = originalRegistryPath }()

	// Test loading non-existent file
	reg, err := LoadProjectsRegistry()
	if err != nil {
		t.Fatalf("LoadProjectsRegistry() error = %v, want nil", err)
	}
	if len(reg.Projects) != 0 {
		t.Errorf("LoadProjectsRegistry() projects length = %d, want 0", len(reg.Projects))
	}

	// Add a project and save
	gitRepo := createTempGitRepo(t)
	if err := reg.Add("test", gitRepo); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	if err := SaveProjectsRegistry(reg); err != nil {
		t.Fatalf("SaveProjectsRegistry() error = %v", err)
	}

	// Load and verify
	loaded, err := LoadProjectsRegistry()
	if err != nil {
		t.Fatalf("LoadProjectsRegistry() error = %v", err)
	}

	if len(loaded.Projects) != 1 {
		t.Errorf("LoadProjectsRegistry() projects length = %d, want 1", len(loaded.Projects))
	}

	if loaded.Projects[0].Name != "test" {
		t.Errorf("LoadProjectsRegistry() project name = %s, want test", loaded.Projects[0].Name)
	}

	if loaded.DefaultProject != "test" {
		t.Errorf("LoadProjectsRegistry() default project = %s, want test", loaded.DefaultProject)
	}
}

func TestDetectProjectFromCwd(t *testing.T) {
	// Create a temporary git repo
	gitRepo := createTempGitRepo(t)

	// Create a subdirectory
	subDir := filepath.Join(gitRepo, "subdir", "nested")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	// Change to subdirectory
	originalCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	defer os.Chdir(originalCwd)

	if err := os.Chdir(subDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}

	// Detect project
	project, err := DetectProjectFromCwd()
	if err != nil {
		t.Fatalf("DetectProjectFromCwd() error = %v", err)
	}

	// Compare evaluated paths to handle /private/var vs /var on macOS
	evalProject, _ := filepath.EvalSymlinks(project.Path)
	evalRepo, _ := filepath.EvalSymlinks(gitRepo)

	if evalProject != evalRepo {
		t.Errorf("DetectProjectFromCwd() path = %s, want %s", evalProject, evalRepo)
	}

	// Test from non-git directory
	nonGitDir := t.TempDir()
	if err := os.Chdir(nonGitDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}

	_, err = DetectProjectFromCwd()
	if err != ErrNotGitRepo {
		t.Errorf("DetectProjectFromCwd() error = %v, want %v", err, ErrNotGitRepo)
	}
}

func TestIsGitRepo(t *testing.T) {
	// Test with git directory
	gitRepo := createTempGitRepo(t)
	if !isGitRepo(gitRepo) {
		t.Errorf("isGitRepo() = false, want true for %s", gitRepo)
	}

	// Test with non-git directory
	nonGit := t.TempDir()
	if isGitRepo(nonGit) {
		t.Errorf("isGitRepo() = true, want false for %s", nonGit)
	}

	// Test with worktree (.git file)
	worktreeDir := t.TempDir()
	gitFile := filepath.Join(worktreeDir, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: /some/path"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if !isGitRepo(worktreeDir) {
		t.Errorf("isGitRepo() = false, want true for worktree %s", worktreeDir)
	}
}

func TestProjectsRegistry_JSON(t *testing.T) {
	reg := &ProjectsRegistry{
		Projects: []Project{
			{Name: "test", Path: "/tmp/test"},
			{Name: "other", Path: "/tmp/other"},
		},
		DefaultProject: "test",
	}

	// Marshal to JSON
	data, err := json.Marshal(reg)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	// Unmarshal from JSON
	var loaded ProjectsRegistry
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	// Verify
	if len(loaded.Projects) != 2 {
		t.Errorf("Unmarshal() projects length = %d, want 2", len(loaded.Projects))
	}

	if loaded.DefaultProject != "test" {
		t.Errorf("Unmarshal() default project = %s, want test", loaded.DefaultProject)
	}
}

// createTempGitRepo creates a temporary directory with a .git subdirectory
func createTempGitRepo(t *testing.T) string {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.Mkdir(gitDir, 0755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	return dir
}
