package processgroup

import (
	"errors"
	"syscall"
	"testing"
)

func TestSelectProcessExitObserverFallsBackForUnsupportedPIDFD(t *testing.T) {
	for _, unsupportedErr := range []error{syscall.ENOSYS, syscall.EINVAL, syscall.EPERM} {
		t.Run(unsupportedErr.Error(), func(t *testing.T) {
			fallbackCalled := false
			exited, err := selectProcessExitObserver(
				func() (<-chan error, error) { return nil, unsupportedErr },
				func() (<-chan error, error) {
					fallbackCalled = true
					result := make(chan error, 1)
					result <- nil
					return result, nil
				},
			)
			if err != nil {
				t.Fatalf("selectProcessExitObserver error = %v", err)
			}
			if !fallbackCalled {
				t.Fatal("non-reaping fallback was not selected")
			}
			if err := <-exited; err != nil {
				t.Fatalf("selected fallback result = %v", err)
			}
		})
	}
}

func TestSelectProcessExitObserverRejectsUnexpectedPIDFDError(t *testing.T) {
	wantErr := syscall.EBADF
	fallbackCalled := false
	_, err := selectProcessExitObserver(
		func() (<-chan error, error) { return nil, wantErr },
		func() (<-chan error, error) {
			fallbackCalled = true
			return nil, nil
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("selectProcessExitObserver error = %v, want %v", err, wantErr)
	}
	if fallbackCalled {
		t.Fatal("fallback selected for unexpected pidfd error")
	}
}

func TestSelectProcessExitObserverPreservesPIDFDObserver(t *testing.T) {
	wantResult := make(chan error, 1)
	wantResult <- nil
	fallbackCalled := false
	exited, err := selectProcessExitObserver(
		func() (<-chan error, error) { return wantResult, nil },
		func() (<-chan error, error) {
			fallbackCalled = true
			return nil, nil
		},
	)
	if err != nil {
		t.Fatalf("selectProcessExitObserver error = %v", err)
	}
	if fallbackCalled {
		t.Fatal("fallback selected when pidfd observer succeeded")
	}
	if exited != wantResult {
		t.Fatal("pidfd observer result was not preserved")
	}
}

func TestObserveProcessExitByStateWaitsForZombieWithoutReaping(t *testing.T) {
	states := []byte{'S', 'D', 'Z'}
	reads := 0
	waits := 0
	exited := observeProcessExitByState(
		42,
		func(int) (byte, error) {
			state := states[reads]
			reads++
			return state, nil
		},
		func() { waits++ },
	)
	if err := <-exited; err != nil {
		t.Fatalf("observeProcessExitByState error = %v", err)
	}
	if reads != len(states) {
		t.Fatalf("process state reads = %d, want %d", reads, len(states))
	}
	if waits != len(states)-1 {
		t.Fatalf("fallback waits = %d, want %d", waits, len(states)-1)
	}
}

func TestObserveProcessExitByStatePropagatesReadFailure(t *testing.T) {
	wantErr := errors.New("proc stat unavailable")
	exited := observeProcessExitByState(
		42,
		func(int) (byte, error) { return 0, wantErr },
		func() { t.Fatal("wait called after process-state read failure") },
	)
	if err := <-exited; !errors.Is(err, wantErr) {
		t.Fatalf("observeProcessExitByState error = %v, want %v", err, wantErr)
	}
}

func TestParseLinuxProcStatHandlesSpacesAndParentheses(t *testing.T) {
	state, processGroup, err := parseLinuxProcStat([]byte("42 (cleanup (worker)) S 1 77 77 0 -1"))
	if err != nil {
		t.Fatalf("parseLinuxProcStat error = %v", err)
	}
	if state != 'S' || processGroup != 77 {
		t.Fatalf("parseLinuxProcStat = state %q group %d, want state S group 77", state, processGroup)
	}
}
