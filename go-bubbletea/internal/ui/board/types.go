package board

import "github.com/riordanpawley/azedarach/internal/domain"

// Column represents a kanban column with tasks
type Column struct {
	Title string
	Tasks []domain.Task
}

// Cursor represents the current cursor position
type Cursor struct {
	Column int // Column index (0-3)
	Task   int // Task index within column
}

// CreatePlaceholderData creates sample data for testing Phase 1 rendering
func CreatePlaceholderData() []Column {
	return []Column{
		{
			Title: "Open",
			Tasks: []domain.Task{
				{
					ID:       "az-1",
					Title:    "Implement user authentication",
					Priority: domain.P2,
					Type:     domain.TypeTask,
					Status:   domain.StatusOpen,
				},
				{
					ID:       "az-2",
					Title:    "Fix login redirect bug",
					Priority: domain.P1,
					Type:     domain.TypeFeature,
					Status:   domain.StatusOpen,
				},
				{
					ID:       "az-5",
					Title:    "Add password reset flow",
					Priority: domain.P3,
					Type:     domain.TypeTask,
					Status:   domain.StatusOpen,
				},
			},
		},
		{
			Title: "In Progress",
			Tasks: []domain.Task{
				{
					ID:       "az-3",
					Title:    "API endpoint refactor",
					Priority: domain.P1,
					Type:     domain.TypeBug,
					Status:   domain.StatusInProgress,
				},
				{
					ID:       "az-4",
					Title:    "Database migration epic",
					Priority: domain.P2,
					Type:     domain.TypeEpic,
					Status:   domain.StatusInProgress,
				},
			},
		},
		{
			Title: "Blocked",
			Tasks: []domain.Task{
				{
					ID:       "az-6",
					Title:    "Deploy to production",
					Priority: domain.P0,
					Type:     domain.TypeTask,
					Status:   domain.StatusBlocked,
				},
			},
		},
		{
			Title: "Done",
			Tasks: []domain.Task{
				{
					ID:       "az-7",
					Title:    "Setup CI/CD pipeline",
					Priority: domain.P3,
					Type:     domain.TypeTask,
					Status:   domain.StatusDone,
				},
				{
					ID:       "az-8",
					Title:    "Configure monitoring",
					Priority: domain.P4,
					Type:     domain.TypeTask,
					Status:   domain.StatusDone,
				},
			},
		},
	}
}
