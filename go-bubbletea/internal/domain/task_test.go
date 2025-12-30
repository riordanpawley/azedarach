package domain

import (
	"testing"
)

func TestStatus_Column(t *testing.T) {
	tests := []struct {
		status Status
		want   int
	}{
		{StatusOpen, 0},
		{StatusInProgress, 1},
		{StatusBlocked, 2},
		{StatusDone, 3},
		{Status("unknown"), 0},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := tt.status.Column(); got != tt.want {
				t.Errorf("Status.Column() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPriority_String(t *testing.T) {
	tests := []struct {
		priority Priority
		want     string
	}{
		{P0, "P0"},
		{P1, "P1"},
		{P2, "P2"},
		{P3, "P3"},
		{P4, "P4"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.priority.String(); got != tt.want {
				t.Errorf("Priority.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTaskType_Short(t *testing.T) {
	tests := []struct {
		taskType TaskType
		want     string
	}{
		{TypeTask, "T"},
		{TypeBug, "B"},
		{TypeFeature, "F"},
		{TypeEpic, "E"},
		{TypeChore, "C"},
		{TaskType("unknown"), "?"},
	}

	for _, tt := range tests {
		t.Run(string(tt.taskType), func(t *testing.T) {
			if got := tt.taskType.Short(); got != tt.want {
				t.Errorf("TaskType.Short() = %v, want %v", got, tt.want)
			}
		})
	}
}
