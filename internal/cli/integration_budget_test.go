package cli

import (
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestIntegrationCommandsUseFullCloseLifecycleBudget(t *testing.T) {
	if issueCloseCleanupTimeout != domain.IntegrationClientTimeout {
		t.Fatalf("issue close client timeout = %v, want %v", issueCloseCleanupTimeout, domain.IntegrationClientTimeout)
	}
	if branchMergeToBaseTimeout != domain.IntegrationClientTimeout {
		t.Fatalf("branch merge client timeout = %v, want %v", branchMergeToBaseTimeout, domain.IntegrationClientTimeout)
	}
	if issueBulkCleanupItemTimeout != domain.IntegrationCloseTimeout {
		t.Fatalf("bulk cleanup item timeout = %v, want %v", issueBulkCleanupItemTimeout, domain.IntegrationCloseTimeout)
	}
	opts, err := ParseIssueCleanupArgs([]string{"--id", "dfq"})
	if err != nil {
		t.Fatalf("parse bulk cleanup defaults: %v", err)
	}
	if opts.PerIssueTimeout != domain.IntegrationCloseTimeout {
		t.Fatalf("bulk cleanup CLI default = %v, want daemon lifecycle budget %v", opts.PerIssueTimeout, domain.IntegrationCloseTimeout)
	}
	preflightBudget := domain.IntegrationCloseReserve
	if got := branchMergeToBaseTimeout - preflightBudget; got < domain.IntegrationMergeTimeout+domain.IntegrationClientReserve {
		t.Fatalf("branch merge remaining budget after preflight = %v, want merge %v plus transport reserve %v", got, domain.IntegrationMergeTimeout, domain.IntegrationClientReserve)
	}
}
