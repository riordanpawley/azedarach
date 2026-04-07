package domain

import (
	"errors"
	"testing"
)

func TestTaskStoreError_Error(t *testing.T) {
	tests := []struct {
		name string
		err  TaskStoreError
		want string
	}{
		{
			name: "with task id",
			err:  TaskStoreError{Op: "update", TaskID: "az-1", Message: "failed"},
			want: "taskstore update [az-1]: failed",
		},
		{
			name: "with task id and underlying error",
			err:  TaskStoreError{Op: "update", TaskID: "az-1", Err: errors.New("constraint failed")},
			want: "taskstore update [az-1]: constraint failed",
		},
		{
			name: "with task id only",
			err:  TaskStoreError{Op: "update", TaskID: "az-1"},
			want: "taskstore update [az-1] failed",
		},
		{
			name: "with message only",
			err:  TaskStoreError{Op: "list", Message: "timeout"},
			want: "taskstore list: timeout",
		},
		{
			name: "with underlying error",
			err:  TaskStoreError{Op: "create", Err: errors.New("connection refused")},
			want: "taskstore create: connection refused",
		},
		{
			name: "minimal",
			err:  TaskStoreError{Op: "search"},
			want: "taskstore search failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("TaskStoreError.Error() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTaskStoreError_Unwrap(t *testing.T) {
	underlying := errors.New("underlying error")
	err := &TaskStoreError{Op: "test", Err: underlying}

	if unwrapped := err.Unwrap(); unwrapped != underlying {
		t.Errorf("Unwrap() = %v, want %v", unwrapped, underlying)
	}
}
