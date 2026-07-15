package domain

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseWorkerEvidencePacketBodyValidDirectPacket(t *testing.T) {
	body := `{
		"schema": "worker_evidence.v1",
		"summary": "Implemented structured evidence packets.",
		"commands_run": ["go test ./internal/domain"],
		"key_assertions": ["packet parser accepts the v1 shape"],
		"files_changed": ["internal/domain/worker_evidence.go"],
		"review": {"status": "clean", "findings": []},
		"risks": ["none"],
		"artifact_links": [{"label": "CI", "url": "https://example.test/run/1"}]
	}`

	packet, result := ParseWorkerEvidencePacketBody(body)
	if !result.Found || !result.Complete {
		t.Fatalf("parse result = %+v, want found complete", result)
	}
	if packet.Schema != WorkerEvidenceSchemaV1 || packet.Summary == "" || len(packet.CommandsRun) != 1 {
		t.Fatalf("packet = %+v", packet)
	}
	if result.Storage != "mailbox_body_json_v1" {
		t.Fatalf("storage = %q", result.Storage)
	}
}

func TestParseWorkerEvidencePacketBodyValidEnvelope(t *testing.T) {
	body := "```json\n" + `{
		"worker_evidence": {
			"schema": "worker_evidence.v1",
			"summary": "Ready for review.",
			"commands_run": ["just test"],
			"key_assertions": ["tests pass"],
			"files_changed": ["cmd/az/main.go"],
			"review": {"status": "findings", "findings": ["fixed parser error path"]},
			"risks": ["none"]
		}
	}` + "\n```"

	packet, result := ParseWorkerEvidencePacketBody(body)
	if !result.Found || !result.Complete {
		t.Fatalf("parse result = %+v, want found complete", result)
	}
	if packet.Review.Status != "findings" || len(packet.Review.Findings) != 1 {
		t.Fatalf("review = %+v", packet.Review)
	}
}

func TestParseWorkerEvidenceIssueEventAcceptsLegacyNestedIntegrationReady(t *testing.T) {
	event := IssueObservationEvent{
		Type: "worker-integration-ready",
		Payload: map[string]any{"worker_evidence": map[string]any{
			"schema": WorkerEvidenceSchemaV1, "summary": "Ready", "commands_run": []string{"just test"},
			"key_assertions": []string{"tests pass"}, "files_changed": []string{"internal/domain/worker_evidence.go"},
			"review": map[string]any{"status": "clean", "findings": []string{}}, "risks": []string{"none"},
		}},
	}
	packet, result := ParseWorkerEvidenceIssueEvent(event)
	if !result.Complete || result.Storage != "issue_event_payload_json_v1" || packet.Summary != "Ready" {
		t.Fatalf("packet=%+v result=%+v, want canonical complete issue-event evidence", packet, result)
	}
}

func TestParseWorkerEvidencePacketBodyReportsIncompletePacket(t *testing.T) {
	body := `{
		"schema": "worker_evidence.v1",
		"summary": "Missing required arrays.",
		"review": {"status": "findings", "findings": []}
	}`

	_, result := ParseWorkerEvidencePacketBody(body)
	if !result.Found || result.Complete {
		t.Fatalf("parse result = %+v, want found incomplete", result)
	}
	problems := strings.Join(result.Problems(), "\n")
	for _, want := range []string{"missing commands_run", "missing files_changed", "missing key_assertions", "missing review.findings", "missing risks"} {
		if !strings.Contains(problems, want) {
			t.Fatalf("problems = %q, missing %q", problems, want)
		}
	}
}

func TestParseWorkerEvidencePacketBodyRejectsArtifactLinksStringArray(t *testing.T) {
	body := `{
		"schema": "worker_evidence.v1",
		"summary": "Ready for integration.",
		"commands_run": ["just test"],
		"key_assertions": ["validation passed"],
		"files_changed": ["internal/domain/worker_evidence.go"],
		"review": {"status": "clean", "findings": []},
		"risks": ["none"],
		"artifact_links": ["https://example.test/run/1"]
	}`

	_, result := ParseWorkerEvidencePacketBody(body)
	if !result.Found || result.Complete {
		t.Fatalf("parse result = %+v, want found incomplete", result)
	}
	problems := strings.Join(result.Problems(), "\n")
	for _, want := range []string{"artifact_links[0] must be an object", "omit artifact_links unless needed", "not a string array"} {
		if !strings.Contains(problems, want) {
			t.Fatalf("problems = %q, missing %q", problems, want)
		}
	}
}

func TestParseWorkerEvidencePacketBodyNormalizesSafeAliases(t *testing.T) {
	body := `{
		"schema": "worker_evidence.v1",
		"summary": "Ready for integration.",
		"commands_run": [{"command": "just test", "result": "passed"}],
		"key_assertions": ["validation passed"],
		"files_changed": ["internal/domain/worker_evidence.go"],
		"review": {"status": "pass", "findings": []},
		"risks": ["none"]
	}`

	packet, result := ParseWorkerEvidencePacketBody(body)
	if !result.Found || !result.Complete {
		t.Fatalf("parse result = %+v, want found complete", result)
	}
	if packet.Review.Status != "clean" {
		t.Fatalf("review status = %q, want clean", packet.Review.Status)
	}
	if len(packet.CommandsRun) != 1 || packet.CommandsRun[0] != "just test (passed)" {
		t.Fatalf("commands_run = %+v, want normalized command result", packet.CommandsRun)
	}
	joined := strings.Join(result.Normalized, "\n")
	for _, want := range []string{"/review/status", "/commands_run"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("normalized = %+v, missing %s", result.Normalized, want)
		}
	}
}

func TestValidateWorkerEvidencePacketBodyReportsPointerDiagnostics(t *testing.T) {
	body := `{
		"schema": "worker_evidence.v1",
		"summary": "Ready for integration.",
		"commands_run": ["just test"],
		"key_assertions": ["validation passed"],
		"files_changed": ["internal/domain/worker_evidence.go"],
		"review": {"status": "shipit", "findings": []},
		"risks": ["none"]
	}`

	result := ValidateWorkerEvidencePacketBody(body, false)
	if result.Complete {
		t.Fatalf("validation = %+v, want incomplete", result)
	}
	var found bool
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Path == "/review/status" && strings.Contains(diagnostic.Message, "review.status") && strings.Join(diagnostic.AllowedValues, ",") == "clean,findings,not_run,blocked" {
			found = true
		}
	}
	if !found {
		t.Fatalf("diagnostics = %+v, want review.status pointer with allowed values", result.Diagnostics)
	}
}

func TestValidateWorkerEvidencePacketBodyFixesRepairablePacket(t *testing.T) {
	body := `{
		"schema": "worker_evidence.v1",
		"summary": "Ready for integration.",
		"commands_run": [{"command": "just test", "result": "passed"}],
		"key_assertions": ["validation passed"],
		"files_changed": ["internal/domain/worker_evidence.go"],
		"review": {"status": "pass"},
		"risks": ["none"],
		"artifact_links": ["https://example.test/run/1"]
	}`

	result := ValidateWorkerEvidencePacketBody(body, true)
	if !result.Complete || !result.Fixed || strings.TrimSpace(result.FixedBody) == "" {
		t.Fatalf("validation = %+v, want fixed complete packet", result)
	}
	if result.Packet == nil || result.Packet.Review.Status != "clean" {
		t.Fatalf("packet = %+v, want clean fixed packet", result.Packet)
	}
	if len(result.Packet.ArtifactLinks) != 1 || result.Packet.ArtifactLinks[0].Label != "artifact 1" {
		t.Fatalf("artifact_links = %+v, want generated object link", result.Packet.ArtifactLinks)
	}
	if !strings.Contains(result.FixedBody, `"artifact_links"`) || !strings.Contains(result.FixedBody, `"findings": []`) {
		t.Fatalf("fixed body = %s, want artifact links and review findings", result.FixedBody)
	}
}

func TestParseWorkerEvidencePacketBodyIgnoresUnstructuredText(t *testing.T) {
	_, result := ParseWorkerEvidencePacketBody("tests pass; ready")
	if result.Found || result.Complete || len(result.Problems()) != 0 {
		t.Fatalf("parse result = %+v, want no packet found", result)
	}
}

func TestWorkerEvidenceRejectsUnleasedOrOverlappedAggregateValidation(t *testing.T) {
	base := `{"schema":"worker_evidence.v1","summary":"ready","commands_run":["just test"],"key_assertions":["tests pass"],"files_changed":["justfile"],"review":{"status":"clean","findings":[]},"risks":["none"],"aggregate_validation":%s}`
	tests := []struct {
		name     string
		evidence string
		complete bool
	}{
		{name: "leased clean", evidence: `{"held":true,"request_id":"req-1","class":"aggregate","profile":"cold","source_revision":"candidate-oid","present":true,"overlap_detected":false,"external_go_processes":0}`, complete: true},
		{name: "unleased", evidence: `{"held":false,"class":"aggregate","present":true,"overlap_detected":false}`, complete: false},
		{name: "overlapped", evidence: `{"held":true,"request_id":"req-2","class":"aggregate","present":true,"overlap_detected":true,"external_go_processes":2}`, complete: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, result := ParseWorkerEvidencePacketBody(fmt.Sprintf(base, tc.evidence))
			if result.Complete != tc.complete {
				t.Fatalf("validation = %+v, complete want %t", result, tc.complete)
			}
		})
	}
}
