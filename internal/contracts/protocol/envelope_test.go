package protocol

import (
	"encoding/json"
	"testing"
)

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

func TestMetadataClientAuditFieldsRoundTripJSON(t *testing.T) {
	meta := Metadata{
		ClientInvocationID: "inv-1",
		ClientCommandShape: "session stop",
		ClientArgv:         []string{"session", "stop", "ckf"},
		ClientExecutable:   "az",
		ClientPID:          123,
		ClientPPID:         45,
		ClientCWD:          "/repo/wt",
		ClientPWD:          "/logical/wt",
		ClientActor:        "riordan",
		ClientUID:          "501",
		ClientActiveIssue:  "ckf",
	}
	payload, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var got Metadata
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got.ClientInvocationID != meta.ClientInvocationID ||
		got.ClientCommandShape != meta.ClientCommandShape ||
		got.ClientExecutable != meta.ClientExecutable ||
		got.ClientPID != meta.ClientPID ||
		got.ClientPPID != meta.ClientPPID ||
		got.ClientCWD != meta.ClientCWD ||
		got.ClientPWD != meta.ClientPWD ||
		got.ClientActor != meta.ClientActor ||
		got.ClientUID != meta.ClientUID ||
		got.ClientActiveIssue != meta.ClientActiveIssue ||
		len(got.ClientArgv) != 3 ||
		got.ClientArgv[2] != "ckf" {
		t.Fatalf("metadata round trip = %+v, want %+v", got, meta)
	}
}
