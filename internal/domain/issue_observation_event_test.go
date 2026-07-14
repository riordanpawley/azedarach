package domain

import "testing"

func TestIssueObservationEventTypeRequiresAuthority(t *testing.T) {
	tests := []struct {
		eventType IssueObservationEventType
		want      bool
	}{
		{eventType: IssueEventReviewCompleted, want: true},
		{eventType: IssueEventReviewCloseFailed, want: true},
		{eventType: IssueEventProgressRecorded, want: false},
	}
	for _, tt := range tests {
		if got := IssueObservationEventTypeRequiresAuthority(tt.eventType); got != tt.want {
			t.Fatalf("IssueObservationEventTypeRequiresAuthority(%q) = %t, want %t", tt.eventType, got, tt.want)
		}
	}
}
