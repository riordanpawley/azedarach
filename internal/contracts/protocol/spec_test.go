package protocol

import (
	"encoding/json"
	"testing"
)

func TestSpecRequirementStatusValid(t *testing.T) {
	tests := []struct {
		name   string
		status SpecRequirementStatus
		want   bool
	}{
		{name: "open", status: SpecRequirementStatusOpen, want: true},
		{name: "accepted", status: SpecRequirementStatusAccepted, want: true},
		{name: "superseded", status: SpecRequirementStatusSuperseded, want: true},
		{name: "unknown", status: SpecRequirementStatus("draft"), want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.status.Valid(); got != tc.want {
				t.Fatalf("Valid() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSpecLinkRoleValid(t *testing.T) {
	tests := []struct {
		name string
		role SpecLinkRole
		want bool
	}{
		{name: "implements", role: SpecLinkRoleImplements, want: true},
		{name: "verifies", role: SpecLinkRoleVerifies, want: true},
		{name: "relates", role: SpecLinkRoleRelates, want: true},
		{name: "unknown", role: SpecLinkRole("blocks"), want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.role.Valid(); got != tc.want {
				t.Fatalf("Valid() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSpecJSONRoundTrip(t *testing.T) {
	title := "Refined title"
	status := SpecRequirementStatusAccepted
	want := struct {
		Update SpecRequirementUpdateRequestBody `json:"update"`
		Read   SpecReadResponseBody             `json:"read"`
		Lint   SpecLintResponseBody             `json:"lint"`
		Parity SpecParityResponseBody           `json:"parity"`
		Sync   SpecSyncMDResponseBody           `json:"sync"`
	}{
		Update: SpecRequirementUpdateRequestBody{
			ID:     "REQ-1",
			Title:  &title,
			Status: &status,
		},
		Read: SpecReadResponseBody{
			Requirements: []SpecRequirement{{
				ID:          "REQ-1",
				Title:       "Requirement one",
				Description: "must work",
				IssueID:     "az-1",
				Status:      SpecRequirementStatusOpen,
			}},
			Links: []SpecLink{{
				ID:      "LINK-1",
				IssueID: "az-1",
				ReqID:   "REQ-1",
				Role:    SpecLinkRoleImplements,
				Note:    "covered by daemon handler",
			}},
		},
		Lint: SpecLintResponseBody{
			OK: false,
			Diagnostics: []SpecDiagnostic{{
				Code:     "missing-link",
				Message:  "requirement is not linked",
				Severity: "error",
				ReqID:    "REQ-2",
			}},
		},
		Parity: SpecParityResponseBody{
			OK: false,
			Findings: []SpecParityFinding{{
				IssueID:  "az-1",
				ReqID:    "REQ-1",
				Severity: "warning",
				Message:  "issue notes drift from linked requirement",
			}},
		},
		Sync: SpecSyncMDResponseBody{
			Target:  "md",
			Check:   true,
			Changed: true,
			Files:   []string{"docs/spec/reqs.md"},
		},
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	var got struct {
		Update SpecRequirementUpdateRequestBody `json:"update"`
		Read   SpecReadResponseBody             `json:"read"`
		Lint   SpecLintResponseBody             `json:"lint"`
		Parity SpecParityResponseBody           `json:"parity"`
		Sync   SpecSyncMDResponseBody           `json:"sync"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if got.Update.Title == nil || *got.Update.Title != title {
		t.Fatalf("Update.Title = %v, want %q", got.Update.Title, title)
	}
	if got.Update.Status == nil || *got.Update.Status != SpecRequirementStatusAccepted {
		t.Fatalf("Update.Status = %v, want %q", got.Update.Status, SpecRequirementStatusAccepted)
	}
	if len(got.Read.Requirements) != 1 || got.Read.Requirements[0].ID != "REQ-1" {
		t.Fatalf("Read.Requirements = %+v", got.Read.Requirements)
	}
	if len(got.Lint.Diagnostics) != 1 || got.Lint.Diagnostics[0].Code != "missing-link" {
		t.Fatalf("Lint.Diagnostics = %+v", got.Lint.Diagnostics)
	}
	if len(got.Parity.Findings) != 1 || got.Parity.Findings[0].ReqID != "REQ-1" {
		t.Fatalf("Parity.Findings = %+v", got.Parity.Findings)
	}
	if got.Sync.Target != "md" || !got.Sync.Check || len(got.Sync.Files) != 1 {
		t.Fatalf("Sync = %+v", got.Sync)
	}
}
