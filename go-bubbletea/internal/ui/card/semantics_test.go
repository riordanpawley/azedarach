package card

import (
	"reflect"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/workflows/session"
)

func TestDeriveBadgesDeterministicOrdering(t *testing.T) {
	t.Parallel()

	model := Model{
		TaskStatus:      domain.StatusInProgress,
		SessionState:    session.StatePaused,
		PullRequest:     PRStateDraft,
		DevServerActive: true,
	}

	got := DeriveBadges(model)
	want := []Badge{
		BadgeTaskInProgress,
		BadgeSessionPaused,
		BadgePRDraft,
		BadgeDevOn,
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DeriveBadges() = %v, want %v", got, want)
	}
}

func TestDeriveBadgesSkipsUnknownButKeepsOrder(t *testing.T) {
	t.Parallel()

	model := Model{
		TaskStatus:      domain.Status("unknown"),
		SessionState:    session.StateBusy,
		PullRequest:     PRState("unknown"),
		DevServerActive: false,
	}

	got := DeriveBadges(model)
	want := []Badge{
		BadgeSessionBusy,
		BadgeDevOff,
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DeriveBadges() = %v, want %v", got, want)
	}
}
