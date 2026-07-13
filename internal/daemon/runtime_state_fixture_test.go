package daemon

import (
	"context"
	"strings"

	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
)

// upsertSessionStateFixture keeps desired intent and observed runtime facts on
// their production authority paths. Tests that provide observed fields are
// explicitly asking to seed a physical fact, not smuggle it through intent.
func upsertSessionStateFixture(store *daemonstate.RuntimeStateStore, ctx context.Context, projectID string, session daemonstate.Session) error {
	observedState := session.ObservedState
	activity, activitySource := session.Activity, session.ActivitySource
	updatedAt := session.UpdatedAt
	if err := store.UpsertSessionState(ctx, projectID, session); err != nil {
		return err
	}
	if strings.TrimSpace(string(observedState)) == "" && strings.TrimSpace(activity) == "" && strings.TrimSpace(activitySource) == "" {
		return nil
	}
	if strings.TrimSpace(string(observedState)) == "" {
		observedState = session.State
	}
	_, _, err := store.ApplyPhysicalSessionObservation(ctx, daemonstate.PhysicalSessionObservation{
		ProjectID: projectID, SessionID: session.ID, ObservedState: observedState,
		Activity: activity, ActivitySource: activitySource, UpdatedAt: updatedAt,
	})
	return err
}
