package protocol

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestNoticeEnumsValid(t *testing.T) {
	if !NoticeSeverityError.Valid() || NoticeSeverity("other").Valid() {
		t.Fatal("notice severity validation mismatch")
	}
	if !NoticeStateActive.Valid() || NoticeState("other").Valid() {
		t.Fatal("notice state validation mismatch")
	}
}

func TestNoticeRecordJSONRoundTrip(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	expiresAt := now.Add(24 * time.Hour)
	want := NoticeRecord{
		NoticeID:  "notice-1",
		ProjectID: "proj",
		Scope: NoticeScope{
			Type: "task",
			ID:   "az-1",
		},
		Source: &NoticeSource{
			OperationID:    "op-1",
			OperationKind:  "task.update_status",
			OperationState: OperationStateFailed,
			RequestID:      naming.RequestID("req-1"),
			Producer:       "daemon",
		},
		Severity:        NoticeSeverityError,
		Category:        "mutation_rejected",
		State:           NoticeStateActive,
		Title:           "Status update failed",
		Summary:         "Could not move az-1 to Done",
		Detail:          "The daemon rejected the mutation.",
		DedupeKey:       "proj:task:az-1:mutation_rejected",
		OccurrenceCount: 2,
		Cause: &NoticeCause{
			Code:      "invalid_transition",
			Message:   "invalid status transition",
			Retryable: true,
			ErrorCode: ErrorCodeConflict,
		},
		Actions: []NoticeAction{{
			ActionID: "dismiss",
			Kind:     "dismiss",
			Label:    "Dismiss",
			Enabled:  true,
			TargetScope: NoticeScope{
				Type: "task",
				ID:   "az-1",
			},
		}},
		FirstOccurrenceAt: now,
		LastOccurrenceAt:  now.Add(time.Minute),
		CreatedAt:         now,
		UpdatedAt:         now.Add(time.Minute),
		ExpiresAt:         &expiresAt,
		RetentionClass:    NoticeRetentionError,
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal notice: %v", err)
	}
	var got NoticeRecord
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal notice: %v", err)
	}
	if got.NoticeID != want.NoticeID || got.ProjectID != want.ProjectID {
		t.Fatalf("notice identity = %s/%s, want %s/%s", got.ProjectID, got.NoticeID, want.ProjectID, want.NoticeID)
	}
	if got.Source == nil || got.Source.OperationID != "op-1" || got.Source.OperationState != OperationStateFailed {
		t.Fatalf("source = %+v, want operation source", got.Source)
	}
	if got.Cause == nil || got.Cause.Code != "invalid_transition" || !got.Cause.Retryable {
		t.Fatalf("cause = %+v, want retryable invalid_transition", got.Cause)
	}
	if len(got.Actions) != 1 || got.Actions[0].ActionID != "dismiss" {
		t.Fatalf("actions = %+v, want dismiss action", got.Actions)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("expires_at = %v, want %v", got.ExpiresAt, expiresAt)
	}
}
