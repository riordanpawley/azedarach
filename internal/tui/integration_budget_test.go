package app

import (
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestTaskCloseMutationUsesSharedClientLifecycleBudget(t *testing.T) {
	if taskCloseMutationTimeout != domain.IntegrationClientTimeout {
		t.Fatalf("TUI close client timeout = %v, want %v", taskCloseMutationTimeout, domain.IntegrationClientTimeout)
	}
	if got := taskStatusMutationTimeout(domain.StatusDone, time.Second); got != domain.IntegrationClientTimeout {
		t.Fatalf("TUI done mutation timeout = %v, want %v", got, domain.IntegrationClientTimeout)
	}
}
