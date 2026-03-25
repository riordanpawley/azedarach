package protocol

import "testing"

func TestErrorCodeTaxonomy(t *testing.T) {
	cases := []struct {
		name          string
		code          ErrorCode
		valid         bool
		retryable     bool
		compatibility bool
	}{
		{name: "invalid request", code: ErrorCodeInvalidRequest, valid: true, retryable: false, compatibility: false},
		{name: "unavailable", code: ErrorCodeUnavailable, valid: true, retryable: true, compatibility: false},
		{name: "revision gap", code: ErrorCodeRevisionGap, valid: true, retryable: true, compatibility: false},
		{name: "upgrade required", code: ErrorCodeUpgradeRequired, valid: true, retryable: false, compatibility: true},
		{name: "unknown literal", code: ErrorCode("other"), valid: false, retryable: false, compatibility: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.code.Valid(); got != tc.valid {
				t.Fatalf("Valid() = %v, want %v", got, tc.valid)
			}
			if got := tc.code.Retryable(); got != tc.retryable {
				t.Fatalf("Retryable() = %v, want %v", got, tc.retryable)
			}
			if got := tc.code.IsCompatibilityFailure(); got != tc.compatibility {
				t.Fatalf("IsCompatibilityFailure() = %v, want %v", got, tc.compatibility)
			}
		})
	}
}
