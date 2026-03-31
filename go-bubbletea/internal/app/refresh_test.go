package app

import (
	"testing"
	"time"
)

func TestRefreshInterval(t *testing.T) {
	if got := refreshInterval(true); got != refreshSuccessInterval {
		t.Fatalf("refreshInterval(true) = %v, want %v", got, refreshSuccessInterval)
	}

	if got := refreshInterval(false); got != refreshFailureInterval {
		t.Fatalf("refreshInterval(false) = %v, want %v", got, refreshFailureInterval)
	}
}

func TestRefreshIntervalOrdering(t *testing.T) {
	if !(refreshSuccessInterval < refreshFailureInterval) {
		t.Fatalf("expected success interval %v to be faster than failure interval %v", refreshSuccessInterval, refreshFailureInterval)
	}

	if refreshSuccessInterval != 2*time.Second || refreshFailureInterval != 5*time.Second {
		t.Fatalf("unexpected refresh intervals: success=%v failure=%v", refreshSuccessInterval, refreshFailureInterval)
	}
}
