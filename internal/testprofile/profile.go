package testprofile

import (
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

// Profile captures a canonical acceptance-test fixture profile.
type Profile struct {
	Name       string
	Width      int
	Height     int
	BaseBranch string
	Tasks      []domain.Task
}

func dep(id string) domain.Dependency {
	dependencyID, err := naming.ParseIssueID(id)
	if err != nil {
		panic(err)
	}
	return domain.Dependency{
		ID:   dependencyID,
		Type: domain.DependencyBlocks,
	}
}

func task(id, title string, status domain.Status, priority domain.Priority, taskType domain.TaskType, deps ...domain.Dependency) domain.Task {
	taskID, err := naming.ParseIssueID(id)
	if err != nil {
		panic(err)
	}
	return domain.Task{
		ID:           taskID,
		Title:        title,
		Status:       status,
		Priority:     priority,
		Type:         taskType,
		Dependencies: deps,
	}
}

var Smoke = Profile{
	Name:       "smoke",
	Width:      18,
	Height:     10,
	BaseBranch: "main",
	Tasks: []domain.Task{
		task("az-smoke-root", "Smoke root", domain.StatusOpen, domain.P2, domain.TypeTask),
		task("az-smoke-child", "Smoke child", domain.StatusOpen, domain.P1, domain.TypeTask, dep("az-smoke-root")),
		task("az-smoke-epic", "Smoke epic", domain.StatusOpen, domain.P1, domain.TypeEpic),
		task("az-smoke-epic-child", "Smoke epic child", domain.StatusOpen, domain.P2, domain.TypeTask, dep("az-smoke-child")),
	},
}

var Integration = Profile{
	Name:       "integration",
	Width:      80,
	Height:     24,
	BaseBranch: "develop",
	Tasks: []domain.Task{
		task("az-int-root-a", "Integration root A", domain.StatusOpen, domain.P2, domain.TypeTask),
		task("az-int-root-b", "Integration root B", domain.StatusOpen, domain.P2, domain.TypeTask),
		task("az-int-child", "Integration child", domain.StatusOpen, domain.P1, domain.TypeFeature, dep("az-int-root-a"), dep("az-int-root-b")),
		task("az-int-independent", "Integration independent", domain.StatusBlocked, domain.P3, domain.TypeBug),
	},
}

func scaleTasks() []domain.Task {
	tasks := []domain.Task{
		task("az-scale-epic", "Scale epic", domain.StatusOpen, domain.P1, domain.TypeEpic),
		task("az-scale-root", "Scale root", domain.StatusOpen, domain.P2, domain.TypeTask),
		task("az-scale-child", "Scale child", domain.StatusOpen, domain.P2, domain.TypeTask, dep("az-scale-root")),
		task("az-scale-hierarchy", "Scale hierarchy child", domain.StatusOpen, domain.P3, domain.TypeTask, dep("az-scale-child")),
	}

	for i := 0; i < 96; i++ {
		tasks = append(tasks, task(
			"az-scale-"+pad3(i),
			"Scale task "+pad3(i),
			domain.StatusOpen,
			domain.P3,
			domain.TypeTask,
		))
	}

	return tasks
}

var Scale = Profile{
	Name:       "scale",
	Width:      160,
	Height:     40,
	BaseBranch: "master",
	Tasks:      scaleTasks(),
}

func pad3(n int) string {
	digits := []byte{'0', '0', '0'}
	if n >= 100 {
		digits[0] = byte('0' + (n/100)%10)
		digits[1] = byte('0' + (n/10)%10)
		digits[2] = byte('0' + n%10)
		return string(digits)
	}
	if n >= 10 {
		digits[1] = byte('0' + (n/10)%10)
		digits[2] = byte('0' + n%10)
		return string(digits)
	}
	digits[2] = byte('0' + n)
	return string(digits)
}
