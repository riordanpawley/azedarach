package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

const WorkerEvidenceSchemaV1 = "worker_evidence.v1"

type WorkerEvidencePacket struct {
	Schema        string                       `json:"schema"`
	Summary       string                       `json:"summary"`
	CommandsRun   []string                     `json:"commands_run"`
	KeyAssertions []string                     `json:"key_assertions"`
	FilesChanged  []string                     `json:"files_changed"`
	Review        WorkerEvidenceReview         `json:"review"`
	Risks         []string                     `json:"risks"`
	ArtifactLinks []WorkerEvidenceArtifactLink `json:"artifact_links,omitempty"`
}

type WorkerEvidenceReview struct {
	Status   string   `json:"status"`
	Findings []string `json:"findings"`
}

type WorkerEvidenceArtifactLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

type WorkerEvidenceParseResult struct {
	Found    bool     `json:"found"`
	Complete bool     `json:"complete"`
	Missing  []string `json:"missing,omitempty"`
	Invalid  []string `json:"invalid,omitempty"`
	Storage  string   `json:"storage"`
}

func (r WorkerEvidenceParseResult) Problems() []string {
	out := make([]string, 0, len(r.Missing)+len(r.Invalid))
	for _, field := range r.Missing {
		out = append(out, "missing "+field)
	}
	out = append(out, r.Invalid...)
	return out
}

func ParseWorkerEvidencePacketBody(body string) (WorkerEvidencePacket, WorkerEvidenceParseResult) {
	result := WorkerEvidenceParseResult{Storage: "mailbox_body_json_v1"}
	raw, ok := workerEvidenceJSONCandidate(body)
	if !ok {
		return WorkerEvidencePacket{}, result
	}
	result.Found = true

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		result.Invalid = append(result.Invalid, fmt.Sprintf("invalid JSON: %v", err))
		return WorkerEvidencePacket{}, result
	}
	if nested, ok := envelope["worker_evidence"]; ok {
		raw = nested
		if err := json.Unmarshal(raw, &envelope); err != nil {
			result.Invalid = append(result.Invalid, fmt.Sprintf("invalid worker_evidence object: %v", err))
			return WorkerEvidencePacket{}, result
		}
	}

	var packet WorkerEvidencePacket
	if err := json.Unmarshal(raw, &packet); err != nil {
		result.Invalid = append(result.Invalid, fmt.Sprintf("invalid worker evidence packet: %v", err))
		return WorkerEvidencePacket{}, result
	}
	result.Missing, result.Invalid = validateWorkerEvidencePacket(packet, envelope)
	result.Complete = len(result.Missing) == 0 && len(result.Invalid) == 0
	return packet, result
}

func validateWorkerEvidencePacket(packet WorkerEvidencePacket, fields map[string]json.RawMessage) ([]string, []string) {
	var missing []string
	var invalid []string
	requiredFields := []string{"schema", "summary", "commands_run", "key_assertions", "files_changed", "review", "risks"}
	for _, field := range requiredFields {
		if _, ok := fields[field]; !ok {
			missing = append(missing, field)
		}
	}
	if strings.TrimSpace(packet.Schema) == "" {
		missing = appendMissing(missing, "schema")
	} else if strings.TrimSpace(packet.Schema) != WorkerEvidenceSchemaV1 {
		invalid = append(invalid, fmt.Sprintf("schema must be %q", WorkerEvidenceSchemaV1))
	}
	if strings.TrimSpace(packet.Summary) == "" {
		missing = appendMissing(missing, "summary")
	}
	if len(nonEmptyStrings(packet.CommandsRun)) == 0 {
		missing = appendMissing(missing, "commands_run")
	}
	if len(nonEmptyStrings(packet.KeyAssertions)) == 0 {
		missing = appendMissing(missing, "key_assertions")
	}
	if len(nonEmptyStrings(packet.FilesChanged)) == 0 {
		missing = appendMissing(missing, "files_changed")
	}
	if strings.TrimSpace(packet.Review.Status) == "" {
		missing = appendMissing(missing, "review.status")
	} else if !validWorkerEvidenceReviewStatus(packet.Review.Status) {
		invalid = append(invalid, "review.status must be one of clean, findings, not_run, or blocked")
	}
	if reviewRaw, ok := fields["review"]; ok {
		var reviewFields map[string]json.RawMessage
		if err := json.Unmarshal(reviewRaw, &reviewFields); err == nil {
			if _, ok := reviewFields["findings"]; !ok {
				missing = appendMissing(missing, "review.findings")
			}
		}
	}
	if strings.EqualFold(strings.TrimSpace(packet.Review.Status), "findings") && len(nonEmptyStrings(packet.Review.Findings)) == 0 {
		missing = appendMissing(missing, "review.findings")
	}
	if len(nonEmptyStrings(packet.Risks)) == 0 {
		missing = appendMissing(missing, "risks")
	}
	for i, link := range packet.ArtifactLinks {
		if strings.TrimSpace(link.Label) == "" {
			missing = appendMissing(missing, fmt.Sprintf("artifact_links[%d].label", i))
		}
		if strings.TrimSpace(link.URL) == "" {
			missing = appendMissing(missing, fmt.Sprintf("artifact_links[%d].url", i))
			continue
		}
		parsed, err := url.Parse(strings.TrimSpace(link.URL))
		if err != nil || parsed.Scheme == "" {
			invalid = append(invalid, fmt.Sprintf("artifact_links[%d].url must be an absolute URL", i))
		}
	}
	sort.Strings(missing)
	return missing, invalid
}

func workerEvidenceJSONCandidate(body string) ([]byte, bool) {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return nil, false
	}
	if strings.HasPrefix(trimmed, "```") {
		lines := strings.Split(trimmed, "\n")
		if len(lines) >= 3 && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
			trimmed = strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
		}
	}
	if !strings.HasPrefix(trimmed, "{") || !strings.HasSuffix(trimmed, "}") {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewBufferString(trimmed))
	decoder.DisallowUnknownFields()
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return []byte(trimmed), true
	}
	if _, ok := raw["worker_evidence"]; ok {
		return []byte(trimmed), true
	}
	if _, ok := raw["schema"]; ok {
		return []byte(trimmed), true
	}
	return nil, false
}

func validWorkerEvidenceReviewStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "clean", "findings", "not_run", "blocked":
		return true
	default:
		return false
	}
}

func nonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

func appendMissing(missing []string, field string) []string {
	for _, existing := range missing {
		if existing == field {
			return missing
		}
	}
	return append(missing, field)
}
