package linearsync

import (
	"fmt"
	"strings"
)

const linearWebhookPublicURLEnv = "LINEAR_WEBHOOK_PUBLIC_URL"

type WebhookFallbackStatus struct {
	Mode    string
	Healthy bool
	Reason  string
}

func (s WebhookFallbackStatus) NormalizedReason() string {
	return normalizeWebhookFallbackReason(s.Reason)
}

func (s WebhookFallbackStatus) MissingPublicWebhookURLCause() bool {
	normalizedReason := s.NormalizedReason()
	if normalizedReason == "" {
		return false
	}
	return isMissingPublicWebhookURLReason(normalizedReason)
}

func (s WebhookFallbackStatus) HealthMessage() string {
	return resolveWebhookFallbackHealthMessage(s.Mode, s.Healthy, s.Reason)
}

func (s WebhookFallbackStatus) ToastMessage() string {
	return resolveWebhookFallbackToastMessage(s.Mode, s.Reason)
}

func normalizeWebhookFallbackReason(reason string) string {
	trimmed := strings.TrimSpace(reason)
	if trimmed == "" {
		return ""
	}
	return trimmed
}

func isMissingPublicWebhookURLReason(reason string) bool {
	normalizedReason := strings.ToLower(reason)
	return strings.Contains(normalizedReason, "public webhook url") ||
		strings.Contains(normalizedReason, strings.ToLower(linearWebhookPublicURLEnv))
}

func resolveWebhookFallbackHealthMessage(mode string, healthy bool, reason string) string {
	normalizedReason := normalizeWebhookFallbackReason(reason)
	if normalizedReason == "" {
		return fmt.Sprintf("SDK mode=%s healthy=%t with no CLI fallback; using background polling.", mode, healthy)
	}
	return fmt.Sprintf("SDK mode=%s healthy=%t with no CLI fallback; reason=%s; using background polling.", mode, healthy, normalizedReason)
}

func resolveWebhookFallbackToastMessage(mode string, reason string) string {
	normalizedReason := normalizeWebhookFallbackReason(reason)
	if normalizedReason == "" {
		return fmt.Sprintf("Linear webhooks unavailable (mode=%s). Falling back to background polling.", mode)
	}

	if mode == "misconfigured" && isMissingPublicWebhookURLReason(normalizedReason) {
		return fmt.Sprintf("Linear webhooks unavailable: %s. Falling back to background polling.", normalizedReason)
	}

	return fmt.Sprintf("Linear webhooks unavailable (mode=%s): %s. Falling back to background polling.", mode, normalizedReason)
}
