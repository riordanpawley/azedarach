package app

import (
	"fmt"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func benchmarkLargeTaskModel(taskCount int) Model {
	m := newTestModel()
	m.loading = false
	m.width = 180
	m.height = 48
	m.tasks = make([]domain.Task, 0, taskCount)
	statuses := []domain.Status{
		domain.StatusOpen,
		domain.StatusInProgress,
		domain.StatusInReview,
		domain.StatusDone,
	}
	priorities := []domain.Priority{domain.P0, domain.P1, domain.P2, domain.P3}
	types := []domain.TaskType{domain.TypeTask, domain.TypeBug, domain.TypeFeature}
	for i := 0; i < taskCount; i++ {
		id := fmt.Sprintf("az-%04d", i)
		task := domain.Task{
			ID:                    naming.IssueID(id),
			Title:                 fmt.Sprintf("Large project task %04d with enough title text to exercise truncation", i),
			Status:                statuses[i%len(statuses)],
			Priority:              priorities[i%len(priorities)],
			Type:                  types[i%len(types)],
			HasWorktree:           i%7 == 0,
			HasTmuxSession:        i%53 == 0,
			GitAheadCount:         i % 5,
			GitBehindCount:        i % 3,
			GitAdditions:          i % 250,
			GitDeletions:          i % 90,
			HasUncommittedChanges: i%11 == 0,
			UpdatedAt:             time.Unix(int64(i), 0).UTC(),
		}
		if i > 0 && i%4 == 0 {
			parentID := naming.IssueID(fmt.Sprintf("az-%04d", i-1))
			task.ParentID = &parentID
		}
		m.tasks = append(m.tasks, task)
	}
	m.nav.SelectTask("az-0000", 0)
	return m
}

func BenchmarkLargeBoardView(b *testing.B) {
	m := benchmarkLargeTaskModel(3000)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = m.View()
	}
}

func BenchmarkLargeBoardKeyUpdateAndView(b *testing.B) {
	m := benchmarkLargeTaskModel(3000)
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		updated, _ := m.Update(msg)
		m = updated.(Model)
		_ = m.View()
	}
}
