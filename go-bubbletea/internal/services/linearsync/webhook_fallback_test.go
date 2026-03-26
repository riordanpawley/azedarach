package linearsync

import "testing"

func TestWebhookFallbackStatusToastMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status WebhookFallbackStatus
		want   string
	}{
		{
			name: "missing public url misconfiguration uses specific wording",
			status: WebhookFallbackStatus{
				Mode:    "misconfigured",
				Healthy: false,
				Reason:  "Linear webhook SDK mode requires a public webhook URL. Set issueTracker.linear.webhooks.url, export LINEAR_WEBHOOK_PUBLIC_URL, or run \"tailscale funnel --bg --yes 9000\"",
			},
			want: "Linear webhooks unavailable: Linear webhook SDK mode requires a public webhook URL. Set issueTracker.linear.webhooks.url, export LINEAR_WEBHOOK_PUBLIC_URL, or run \"tailscale funnel --bg --yes 9000\". Falling back to background polling.",
		},
		{
			name: "non url cause stays generic",
			status: WebhookFallbackStatus{
				Mode:    "misconfigured",
				Healthy: false,
				Reason:  "Timed out registering Linear webhook after 4000ms",
			},
			want: "Linear webhooks unavailable (mode=misconfigured): Timed out registering Linear webhook after 4000ms. Falling back to background polling.",
		},
		{
			name: "blank cause collapses to mode only",
			status: WebhookFallbackStatus{
				Mode:    "failed",
				Healthy: false,
				Reason:  "   ",
			},
			want: "Linear webhooks unavailable (mode=failed). Falling back to background polling.",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.status.ToastMessage(); got != tt.want {
				t.Fatalf("ToastMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWebhookFallbackStatusHealthMessage(t *testing.T) {
	t.Parallel()

	status := WebhookFallbackStatus{
		Mode:    "failed",
		Healthy: false,
		Reason:  "Timed out registering Linear webhook after 4000ms",
	}

	want := "SDK mode=failed healthy=false with no CLI fallback; reason=Timed out registering Linear webhook after 4000ms; using background polling."
	if got := status.HealthMessage(); got != want {
		t.Fatalf("HealthMessage() = %q, want %q", got, want)
	}
}

func TestMissingPublicWebhookURLCauseDetection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason string
		want   bool
	}{
		{
			name:   "matches explicit public webhook url wording",
			reason: "Linear webhook SDK mode requires a public webhook URL. Set issueTracker.linear.webhooks.url, export LINEAR_WEBHOOK_PUBLIC_URL, or run \"tailscale funnel --bg --yes 9000\"",
			want:   true,
		},
		{
			name:   "matches env var wording",
			reason: "Missing LINEAR_WEBHOOK_PUBLIC_URL prevents webhook fallback",
			want:   true,
		},
		{
			name:   "does not match similar but private url wording",
			reason: "Linear webhook URL is unavailable in private mode",
			want:   false,
		},
		{
			name:   "blank reason does not match",
			reason: "   ",
			want:   false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isMissingPublicWebhookURLReason(normalizeWebhookFallbackReason(tt.reason)); got != tt.want {
				t.Fatalf("isMissingPublicWebhookURLReason(%q) = %v, want %v", tt.reason, got, tt.want)
			}
		})
	}
}
