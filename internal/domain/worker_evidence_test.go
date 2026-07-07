package domain

import (
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

func TestParseWorkerEvidencePacketBodyIgnoresUnstructuredText(t *testing.T) {
	_, result := ParseWorkerEvidencePacketBody("tests pass; ready")
	if result.Found || result.Complete || len(result.Problems()) != 0 {
		t.Fatalf("parse result = %+v, want no packet found", result)
	}
}
