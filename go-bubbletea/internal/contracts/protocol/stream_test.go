package protocol

import "testing"

func TestStreamProjectionDecisionTaxonomy(t *testing.T) {
	cases := []struct {
		name     string
		decision StreamProjectionDecision
		valid    bool
	}{
		{name: "ignore", decision: StreamProjectionDecisionIgnore, valid: true},
		{name: "apply", decision: StreamProjectionDecisionApply, valid: true},
		{name: "resync", decision: StreamProjectionDecisionResync, valid: true},
		{name: "unknown", decision: StreamProjectionDecision(99), valid: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.decision.Valid(); got != tc.valid {
				t.Fatalf("Valid() = %v, want %v", got, tc.valid)
			}
		})
	}
}

func TestStreamCursorDecideAndAdvance(t *testing.T) {
	cursor := StreamCursor{Revision: 4}

	tests := []struct {
		name    string
		evt     EventEnvelope
		want    StreamProjectionDecision
		wantRev uint64
	}{
		{
			name:    "duplicate",
			evt:     EventEnvelope{Revision: 4},
			want:    StreamProjectionDecisionIgnore,
			wantRev: 4,
		},
		{
			name:    "stale",
			evt:     EventEnvelope{Revision: 3},
			want:    StreamProjectionDecisionIgnore,
			wantRev: 4,
		},
		{
			name:    "sequential",
			evt:     EventEnvelope{Revision: 5},
			want:    StreamProjectionDecisionApply,
			wantRev: 5,
		},
		{
			name:    "gap",
			evt:     EventEnvelope{Revision: 7},
			want:    StreamProjectionDecisionResync,
			wantRev: 4,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cursor.Decide(tc.evt); got != tc.want {
				t.Fatalf("Decide() = %v, want %v", got, tc.want)
			}
			if got := cursor.Advance(tc.evt).Revision; got != tc.wantRev {
				t.Fatalf("Advance().Revision = %d, want %d", got, tc.wantRev)
			}
		})
	}
}
