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
	Schema              string                       `json:"schema"`
	Summary             string                       `json:"summary"`
	CommandsRun         []string                     `json:"commands_run"`
	KeyAssertions       []string                     `json:"key_assertions"`
	FilesChanged        []string                     `json:"files_changed"`
	Review              WorkerEvidenceReview         `json:"review"`
	Risks               []string                     `json:"risks"`
	ArtifactLinks       []WorkerEvidenceArtifactLink `json:"artifact_links,omitempty"`
	AggregateValidation *ValidationEvidence          `json:"aggregate_validation,omitempty"`
}

type WorkerEvidenceReview struct {
	Status          string                      `json:"status"`
	Findings        []string                    `json:"findings"`
	Revision        string                      `json:"revision,omitempty"`
	Angle           string                      `json:"angle,omitempty"`
	ReusedLayers    []string                    `json:"reused_layers,omitempty"`
	Matrix          *WorkerEvidenceReviewMatrix `json:"matrix,omitempty"`
	ExtraPassReason string                      `json:"extra_pass_reason,omitempty"`
	FallbackReason  string                      `json:"fallback_reason,omitempty"`
	CleanPass       *int                        `json:"clean_pass,omitempty"`
	CleanPassTarget *int                        `json:"clean_pass_target,omitempty"`
}

type WorkerEvidenceReviewMatrix struct {
	Type         string                              `json:"type,omitempty"`
	CoveredCells []string                            `json:"covered_cells,omitempty"`
	SkippedCells []WorkerEvidenceReviewSkippedMatrix `json:"skipped_cells"`
}

type WorkerEvidenceReviewSkippedMatrix struct {
	Cell   string `json:"cell"`
	Reason string `json:"reason"`
}

type WorkerEvidenceArtifactLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

type WorkerEvidenceDiagnostic struct {
	Path          string   `json:"path"`
	Message       string   `json:"message"`
	AllowedValues []string `json:"allowed_values,omitempty"`
	Suggestion    string   `json:"suggestion,omitempty"`
}

type WorkerEvidenceParseResult struct {
	Found       bool                       `json:"found"`
	Complete    bool                       `json:"complete"`
	Missing     []string                   `json:"missing,omitempty"`
	Invalid     []string                   `json:"invalid,omitempty"`
	Diagnostics []WorkerEvidenceDiagnostic `json:"diagnostics,omitempty"`
	Normalized  []string                   `json:"normalized,omitempty"`
	Storage     string                     `json:"storage"`
}

type WorkerEvidenceValidationResult struct {
	Found       bool                       `json:"found"`
	Complete    bool                       `json:"complete"`
	Missing     []string                   `json:"missing,omitempty"`
	Invalid     []string                   `json:"invalid,omitempty"`
	Diagnostics []WorkerEvidenceDiagnostic `json:"diagnostics,omitempty"`
	Normalized  []string                   `json:"normalized,omitempty"`
	Packet      *WorkerEvidencePacket      `json:"packet,omitempty"`
	Fixed       bool                       `json:"fixed"`
	FixedBody   string                     `json:"fixed_body,omitempty"`
	Template    WorkerEvidencePacket       `json:"template"`
	Storage     string                     `json:"storage"`
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
			result.Diagnostics = append(result.Diagnostics, WorkerEvidenceDiagnostic{
				Path:       "/worker_evidence",
				Message:    fmt.Sprintf("invalid worker_evidence object: %v", err),
				Suggestion: "send a JSON object matching worker_evidence.v1",
			})
			return WorkerEvidencePacket{}, result
		}
	}
	envelope, result.Diagnostics, result.Normalized = normalizeWorkerEvidenceFields(envelope, workerEvidenceNormalizeMode{
		ReviewStatusAliases: true,
		CommandRecords:      true,
	})
	if len(result.Normalized) > 0 {
		var err error
		raw, err = json.Marshal(envelope)
		if err != nil {
			result.Invalid = append(result.Invalid, fmt.Sprintf("invalid worker evidence packet: %v", err))
			result.Diagnostics = append(result.Diagnostics, WorkerEvidenceDiagnostic{Path: "", Message: err.Error()})
			return WorkerEvidencePacket{}, result
		}
	}
	if invalid := validateWorkerEvidenceRawShape(envelope); len(invalid) > 0 {
		result.Invalid = append(result.Invalid, invalid...)
		result.Diagnostics = append(result.Diagnostics, diagnosticsForInvalidWorkerEvidence(invalid)...)
		return WorkerEvidencePacket{}, result
	}

	var packet WorkerEvidencePacket
	if err := json.Unmarshal(raw, &packet); err != nil {
		result.Invalid = append(result.Invalid, fmt.Sprintf("invalid worker evidence packet: %v", err))
		result.Diagnostics = append(result.Diagnostics, WorkerEvidenceDiagnostic{
			Path:       "",
			Message:    fmt.Sprintf("invalid worker evidence packet: %v", err),
			Suggestion: "run `az mail validate-evidence --fix` to preview a canonical packet when the mismatch is repairable",
		})
		return WorkerEvidencePacket{}, result
	}
	result.Missing, result.Invalid = validateWorkerEvidencePacket(packet, envelope)
	result.Diagnostics = append(result.Diagnostics, diagnosticsForMissingWorkerEvidence(result.Missing)...)
	result.Diagnostics = append(result.Diagnostics, diagnosticsForInvalidWorkerEvidence(result.Invalid)...)
	result.Complete = len(result.Missing) == 0 && len(result.Invalid) == 0
	return packet, result
}

// IsWorkerEvidenceEventType reports whether an issue observation event is a
// durable worker-evidence submission. The hyphenated spellings are retained
// for compatibility with workers that recorded mailbox event names directly.
func IsWorkerEvidenceEventType(eventType IssueObservationEventType) bool {
	normalized := strings.NewReplacer("_", ".", "-", ".").Replace(strings.ToLower(strings.TrimSpace(string(eventType))))
	switch normalized {
	case string(IssueEventEvidenceSubmitted), "worker.integration.ready", "worker.ready", "worker.complete":
		return true
	default:
		return false
	}
}

// ParseWorkerEvidenceIssueEvent applies the canonical worker_evidence.v1
// parser to durable issue-event storage. It accepts both the direct canonical
// payload and the legacy {"worker_evidence": {...}} envelope.
func ParseWorkerEvidenceIssueEvent(event IssueObservationEvent) (WorkerEvidencePacket, WorkerEvidenceParseResult) {
	result := WorkerEvidenceParseResult{Storage: "issue_event_payload_json_v1"}
	if !IsWorkerEvidenceEventType(event.Type) {
		return WorkerEvidencePacket{}, result
	}
	body, err := json.Marshal(event.Payload)
	if err != nil {
		result.Found = true
		result.Invalid = []string{fmt.Sprintf("marshal issue event payload: %v", err)}
		result.Diagnostics = []WorkerEvidenceDiagnostic{{Path: "", Message: result.Invalid[0]}}
		return WorkerEvidencePacket{}, result
	}
	packet, result := ParseWorkerEvidencePacketBody(string(body))
	result.Storage = "issue_event_payload_json_v1"
	return packet, result
}

// WorkerEvidencePacketPayload returns the canonical direct issue-event storage
// shape for a parsed packet.
func WorkerEvidencePacketPayload(packet WorkerEvidencePacket) (map[string]any, error) {
	body, err := json.Marshal(packet)
	if err != nil {
		return nil, fmt.Errorf("marshal worker evidence packet: %w", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode worker evidence payload: %w", err)
	}
	return payload, nil
}

func ValidateWorkerEvidencePacketBody(body string, fix bool) WorkerEvidenceValidationResult {
	packet, parsed := ParseWorkerEvidencePacketBody(body)
	result := WorkerEvidenceValidationResult{
		Found:       parsed.Found,
		Complete:    parsed.Complete,
		Missing:     append([]string(nil), parsed.Missing...),
		Invalid:     append([]string(nil), parsed.Invalid...),
		Diagnostics: append([]WorkerEvidenceDiagnostic(nil), parsed.Diagnostics...),
		Normalized:  append([]string(nil), parsed.Normalized...),
		Template:    WorkerEvidencePacketTemplate(),
		Storage:     parsed.Storage,
	}
	if parsed.Complete {
		result.Packet = &packet
		if fix && len(parsed.Normalized) > 0 {
			if data, err := json.MarshalIndent(packet, "", "  "); err == nil {
				result.Fixed = true
				result.FixedBody = string(data)
			}
		}
		return result
	}
	if !fix {
		return result
	}
	fixedBody, fixed, fixDiagnostics := repairWorkerEvidencePacketBody(body)
	result.Diagnostics = append(result.Diagnostics, fixDiagnostics...)
	if !fixed {
		return result
	}
	fixedPacket, fixedParsed := ParseWorkerEvidencePacketBody(fixedBody)
	result.Fixed = fixedParsed.Complete
	result.FixedBody = fixedBody
	result.Complete = fixedParsed.Complete
	result.Missing = append([]string(nil), fixedParsed.Missing...)
	result.Invalid = append([]string(nil), fixedParsed.Invalid...)
	result.Normalized = append(result.Normalized, fixedParsed.Normalized...)
	result.Diagnostics = append(result.Diagnostics, fixedParsed.Diagnostics...)
	if fixedParsed.Complete {
		result.Packet = &fixedPacket
	}
	return result
}

func WorkerEvidencePacketTemplate() WorkerEvidencePacket {
	return WorkerEvidencePacket{
		Schema:        WorkerEvidenceSchemaV1,
		Summary:       "Ready for integration.",
		CommandsRun:   []string{"<project validation command>"},
		KeyAssertions: []string{"validation passed"},
		FilesChanged:  []string{"path/to/changed-file"},
		Review: WorkerEvidenceReview{
			Status:          "clean",
			Findings:        []string{},
			Revision:        "<exact candidate revision>",
			Angle:           "complete worker review",
			ReusedLayers:    []string{"none"},
			Matrix:          &WorkerEvidenceReviewMatrix{Type: "general", CoveredCells: []string{"requested behavior", "integration points", "error paths", "affected consumers"}, SkippedCells: []WorkerEvidenceReviewSkippedMatrix{}},
			CleanPass:       workerEvidenceInt(1),
			CleanPassTarget: workerEvidenceInt(1),
		},
		Risks: []string{"none"},
	}
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
			detailMissing, detailInvalid := validateWorkerEvidenceReviewDetails(packet.Review, reviewFields)
			missing = append(missing, detailMissing...)
			invalid = append(invalid, detailInvalid...)
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
	if evidence := packet.AggregateValidation; evidence != nil {
		if evidence.Class != ValidationClassAggregate {
			invalid = append(invalid, "aggregate_validation.class must be aggregate")
		}
		if !evidence.Held || strings.TrimSpace(evidence.RequestID) == "" {
			invalid = append(invalid, "aggregate_validation must identify a held daemon validation request")
		}
		if strings.TrimSpace(evidence.SourceRevision) == "" {
			invalid = append(invalid, "aggregate_validation.source_revision is required")
		}
		if !evidence.Present {
			invalid = append(invalid, "aggregate_validation must include machine-load evidence")
		}
		if evidence.Purpose == ValidationPurposeCapacity && evidence.OverlapDetected {
			invalid = append(invalid, "capacity aggregate_validation must not overlap external Go processes")
		}
	}
	sort.Strings(missing)
	return missing, invalid
}

func validateWorkerEvidenceReviewDetails(review WorkerEvidenceReview, fields map[string]json.RawMessage) ([]string, []string) {
	detailFields := []string{"revision", "angle", "reused_layers", "matrix", "extra_pass_reason", "fallback_reason", "clean_pass", "clean_pass_target"}
	structured := false
	for _, field := range detailFields {
		if _, ok := fields[field]; ok {
			structured = true
			break
		}
	}
	if !structured {
		return nil, nil // Historical worker_evidence.v1 packets remain valid.
	}

	var missing []string
	var invalid []string
	for _, field := range []string{"revision", "angle", "reused_layers", "matrix", "clean_pass", "clean_pass_target"} {
		if _, ok := fields[field]; !ok {
			missing = appendMissing(missing, "review."+field)
		}
	}
	if strings.TrimSpace(review.Revision) == "" {
		missing = appendMissing(missing, "review.revision")
	}
	if strings.TrimSpace(review.Angle) == "" {
		missing = appendMissing(missing, "review.angle")
	}
	if len(nonEmptyStrings(review.ReusedLayers)) == 0 {
		missing = appendMissing(missing, "review.reused_layers")
	}
	if duplicate := firstDuplicateNonEmptyString(review.Findings); duplicate != "" {
		invalid = append(invalid, fmt.Sprintf("review.findings must be deduplicated; repeated %q", duplicate))
	}
	if review.Matrix == nil || strings.TrimSpace(review.Matrix.Type) == "" {
		missing = appendMissing(missing, "review.matrix.type")
	} else if !validWorkerEvidenceReviewMatrixType(review.Matrix.Type) {
		invalid = append(invalid, "review.matrix.type must be one of general, stateful/concurrent, subprocess, or persistence")
	}
	if review.Matrix == nil || len(nonEmptyStrings(review.Matrix.CoveredCells)) == 0 {
		missing = appendMissing(missing, "review.matrix.covered_cells")
	}
	if matrixRaw, ok := fields["matrix"]; ok {
		var matrixFields map[string]json.RawMessage
		if err := json.Unmarshal(matrixRaw, &matrixFields); err == nil {
			if skippedRaw, ok := matrixFields["skipped_cells"]; !ok || strings.TrimSpace(string(skippedRaw)) == "null" {
				missing = appendMissing(missing, "review.matrix.skipped_cells")
			}
		}
	}
	if review.Matrix != nil {
		for i, skipped := range review.Matrix.SkippedCells {
			if strings.TrimSpace(skipped.Cell) == "" {
				missing = appendMissing(missing, fmt.Sprintf("review.matrix.skipped_cells[%d].cell", i))
			}
			if strings.TrimSpace(skipped.Reason) == "" {
				missing = appendMissing(missing, fmt.Sprintf("review.matrix.skipped_cells[%d].reason", i))
			}
		}
	}
	if review.CleanPass == nil {
		missing = appendMissing(missing, "review.clean_pass")
	}
	if review.CleanPassTarget == nil {
		missing = appendMissing(missing, "review.clean_pass_target")
	}
	cleanPass := 0
	if review.CleanPass != nil {
		cleanPass = *review.CleanPass
	}
	cleanPassTarget := 0
	if review.CleanPassTarget != nil {
		cleanPassTarget = *review.CleanPassTarget
	}
	if cleanPass < 0 {
		invalid = append(invalid, "review.clean_pass cannot be negative")
	}
	if strings.EqualFold(strings.TrimSpace(review.Status), "clean") && cleanPass < 1 {
		invalid = append(invalid, "review.clean_pass must be at least 1 for a clean review")
	}
	if cleanPassTarget < 1 {
		invalid = append(invalid, "review.clean_pass_target must be at least 1")
	}
	if cleanPass > cleanPassTarget && cleanPassTarget > 0 {
		invalid = append(invalid, "review.clean_pass cannot exceed review.clean_pass_target")
	}
	if cleanPassTarget > 1 && strings.TrimSpace(review.ExtraPassReason) == "" {
		missing = appendMissing(missing, "review.extra_pass_reason")
	}
	return missing, invalid
}

func validWorkerEvidenceReviewMatrixType(matrixType string) bool {
	switch strings.ToLower(strings.TrimSpace(matrixType)) {
	case "general", "stateful/concurrent", "subprocess", "persistence":
		return true
	default:
		return false
	}
}

func firstDuplicateNonEmptyString(values []string) string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			return trimmed
		}
		seen[key] = struct{}{}
	}
	return ""
}

func workerEvidenceInt(value int) *int {
	return &value
}

func validateWorkerEvidenceRawShape(fields map[string]json.RawMessage) []string {
	rawLinks, ok := fields["artifact_links"]
	if !ok || strings.TrimSpace(string(rawLinks)) == "null" {
		return nil
	}
	var links []json.RawMessage
	if err := json.Unmarshal(rawLinks, &links); err != nil {
		return []string{"artifact_links must be an array of objects with label and url fields; omit artifact_links unless needed, or use [{\"label\":\"...\",\"url\":\"https://...\"}], not a string array"}
	}
	for i, raw := range links {
		trimmed := strings.TrimSpace(string(raw))
		if !strings.HasPrefix(trimmed, "{") {
			return []string{fmt.Sprintf("artifact_links[%d] must be an object with label and url fields; omit artifact_links unless needed, or use [{\"label\":\"...\",\"url\":\"https://...\"}], not a string array", i)}
		}
	}
	return nil
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

func allowedWorkerEvidenceReviewStatuses() []string {
	return []string{"clean", "findings", "not_run", "blocked"}
}

type workerEvidenceNormalizeMode struct {
	ReviewStatusAliases bool
	CommandRecords      bool
	ArtifactStringLinks bool
	FillReviewFindings  bool
}

func normalizeWorkerEvidenceFields(fields map[string]json.RawMessage, mode workerEvidenceNormalizeMode) (map[string]json.RawMessage, []WorkerEvidenceDiagnostic, []string) {
	out := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		out[key] = append(json.RawMessage(nil), value...)
	}
	var diagnostics []WorkerEvidenceDiagnostic
	var normalized []string

	if rawReview, ok := out["review"]; ok {
		reviewFields := map[string]json.RawMessage{}
		if err := json.Unmarshal(rawReview, &reviewFields); err == nil {
			if rawStatus, ok := reviewFields["status"]; ok {
				var status string
				if err := json.Unmarshal(rawStatus, &status); err == nil {
					if canonical, ok := workerEvidenceReviewStatusAlias(status); ok {
						diagnostics = append(diagnostics, WorkerEvidenceDiagnostic{
							Path:          "/review/status",
							Message:       fmt.Sprintf("review.status %q is an alias for %q", strings.TrimSpace(status), canonical),
							AllowedValues: allowedWorkerEvidenceReviewStatuses(),
							Suggestion:    fmt.Sprintf("use %q", canonical),
						})
						if mode.ReviewStatusAliases {
							reviewFields["status"] = mustWorkerEvidenceJSONRaw(canonical)
							normalized = append(normalized, "/review/status")
						}
					}
				}
			}
			if mode.FillReviewFindings {
				if _, ok := reviewFields["findings"]; !ok {
					if statusRaw, ok := reviewFields["status"]; ok {
						var status string
						if err := json.Unmarshal(statusRaw, &status); err == nil && !strings.EqualFold(strings.TrimSpace(status), "findings") {
							reviewFields["findings"] = mustWorkerEvidenceJSONRaw([]string{})
							normalized = append(normalized, "/review/findings")
							diagnostics = append(diagnostics, WorkerEvidenceDiagnostic{
								Path:       "/review/findings",
								Message:    "review.findings is required even when there are no findings",
								Suggestion: "use an empty array for clean, not_run, or blocked reviews",
							})
						}
					}
				}
			}
			if data, err := json.Marshal(reviewFields); err == nil {
				out["review"] = data
			}
		}
	}

	if rawCommands, ok := out["commands_run"]; ok {
		commands, changed, commandDiagnostics := normalizeWorkerEvidenceCommands(rawCommands, mode.CommandRecords)
		diagnostics = append(diagnostics, commandDiagnostics...)
		if changed {
			out["commands_run"] = mustWorkerEvidenceJSONRaw(commands)
			normalized = append(normalized, "/commands_run")
		}
	}

	if rawLinks, ok := out["artifact_links"]; ok {
		links, changed, linkDiagnostics := normalizeWorkerEvidenceArtifactLinks(rawLinks, mode.ArtifactStringLinks)
		diagnostics = append(diagnostics, linkDiagnostics...)
		if changed {
			out["artifact_links"] = mustWorkerEvidenceJSONRaw(links)
			normalized = append(normalized, "/artifact_links")
		}
	}

	return out, diagnostics, normalized
}

func workerEvidenceReviewStatusAlias(status string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pass", "passed":
		return "clean", true
	default:
		return "", false
	}
}

func normalizeWorkerEvidenceCommands(raw json.RawMessage, fix bool) ([]string, bool, []WorkerEvidenceDiagnostic) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false, []WorkerEvidenceDiagnostic{{
			Path:       "/commands_run",
			Message:    "commands_run must be an array",
			Suggestion: "use an array of command strings, or run with --fix for supported structured command records",
		}}
	}
	out := make([]string, 0, len(items))
	changed := false
	unrepairable := false
	var diagnostics []WorkerEvidenceDiagnostic
	for i, item := range items {
		path := fmt.Sprintf("/commands_run/%d", i)
		var command string
		if err := json.Unmarshal(item, &command); err == nil {
			out = append(out, command)
			continue
		}
		var record struct {
			Command string `json:"command"`
			Result  string `json:"result"`
		}
		if err := json.Unmarshal(item, &record); err == nil && strings.TrimSpace(record.Command) != "" {
			value := strings.TrimSpace(record.Command)
			if result := strings.TrimSpace(record.Result); result != "" {
				value += " (" + result + ")"
			}
			diagnostics = append(diagnostics, WorkerEvidenceDiagnostic{
				Path:       path,
				Message:    "commands_run structured records are normalized to command strings",
				Suggestion: `use "command" strings in the canonical packet, for example "project-check test"`,
			})
			if fix {
				out = append(out, value)
				changed = true
			}
			continue
		}
		diagnostics = append(diagnostics, WorkerEvidenceDiagnostic{
			Path:       path,
			Message:    "commands_run entries must be strings or objects with a non-empty command field",
			Suggestion: `use "project-check test" or {"command":"project-check test","result":"passed"}`,
		})
		unrepairable = true
	}
	if unrepairable {
		return nil, false, diagnostics
	}
	return out, changed, diagnostics
}

func normalizeWorkerEvidenceArtifactLinks(raw json.RawMessage, fix bool) ([]WorkerEvidenceArtifactLink, bool, []WorkerEvidenceDiagnostic) {
	if strings.TrimSpace(string(raw)) == "null" {
		return nil, false, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false, []WorkerEvidenceDiagnostic{{
			Path:       "/artifact_links",
			Message:    "artifact_links must be an array of objects with label and url fields",
			Suggestion: `omit artifact_links unless needed, or use [{"label":"CI","url":"https://example.test/run"}]`,
		}}
	}
	out := make([]WorkerEvidenceArtifactLink, 0, len(items))
	changed := false
	var diagnostics []WorkerEvidenceDiagnostic
	for i, item := range items {
		path := fmt.Sprintf("/artifact_links/%d", i)
		var link WorkerEvidenceArtifactLink
		if err := json.Unmarshal(item, &link); err == nil && strings.HasPrefix(strings.TrimSpace(string(item)), "{") {
			out = append(out, link)
			continue
		}
		var urlValue string
		if err := json.Unmarshal(item, &urlValue); err == nil && strings.TrimSpace(urlValue) != "" {
			diagnostics = append(diagnostics, WorkerEvidenceDiagnostic{
				Path:       path,
				Message:    "artifact_links entries must be objects with label and url fields",
				Suggestion: `use {"label":"artifact 1","url":"` + strings.TrimSpace(urlValue) + `"}`,
			})
			if fix {
				out = append(out, WorkerEvidenceArtifactLink{
					Label: fmt.Sprintf("artifact %d", i+1),
					URL:   strings.TrimSpace(urlValue),
				})
				changed = true
			}
			continue
		}
		diagnostics = append(diagnostics, WorkerEvidenceDiagnostic{
			Path:       path,
			Message:    "artifact_links entries must be objects with label and url fields",
			Suggestion: `omit artifact_links unless needed, or use [{"label":"CI","url":"https://example.test/run"}]`,
		})
	}
	return out, changed, diagnostics
}

func repairWorkerEvidencePacketBody(body string) (string, bool, []WorkerEvidenceDiagnostic) {
	raw, ok := workerEvidenceJSONCandidate(body)
	if !ok {
		return "", false, []WorkerEvidenceDiagnostic{{
			Path:       "",
			Message:    "body is not a JSON worker_evidence.v1 packet",
			Suggestion: "provide a JSON packet or run with --template for the canonical schema",
		}}
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", false, []WorkerEvidenceDiagnostic{{Path: "", Message: fmt.Sprintf("invalid JSON: %v", err)}}
	}
	nested := false
	if nestedRaw, ok := envelope["worker_evidence"]; ok {
		nested = true
		raw = nestedRaw
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return "", false, []WorkerEvidenceDiagnostic{{Path: "/worker_evidence", Message: fmt.Sprintf("invalid worker_evidence object: %v", err)}}
		}
	}
	normalized, diagnostics, changedFields := normalizeWorkerEvidenceFields(envelope, workerEvidenceNormalizeMode{
		ReviewStatusAliases: true,
		CommandRecords:      true,
		ArtifactStringLinks: true,
		FillReviewFindings:  true,
	})
	if len(changedFields) == 0 {
		return "", false, diagnostics
	}
	var fixed any = normalized
	if nested {
		fixed = map[string]any{"worker_evidence": normalized}
	}
	data, err := json.MarshalIndent(fixed, "", "  ")
	if err != nil {
		return "", false, append(diagnostics, WorkerEvidenceDiagnostic{Path: "", Message: err.Error()})
	}
	return string(data), true, diagnostics
}

func diagnosticsForMissingWorkerEvidence(missing []string) []WorkerEvidenceDiagnostic {
	diagnostics := make([]WorkerEvidenceDiagnostic, 0, len(missing))
	for _, field := range missing {
		diagnostics = append(diagnostics, WorkerEvidenceDiagnostic{
			Path:       workerEvidenceJSONPointer(field),
			Message:    "required field is missing or empty",
			Suggestion: workerEvidenceSuggestionForField(field),
		})
	}
	return diagnostics
}

func diagnosticsForInvalidWorkerEvidence(invalid []string) []WorkerEvidenceDiagnostic {
	diagnostics := make([]WorkerEvidenceDiagnostic, 0, len(invalid))
	for _, reason := range invalid {
		diagnostic := WorkerEvidenceDiagnostic{
			Path:       "",
			Message:    reason,
			Suggestion: "run `az mail validate-evidence --fix` to preview a canonical packet when the mismatch is repairable",
		}
		switch {
		case strings.Contains(reason, "review.status"):
			diagnostic.Path = "/review/status"
			diagnostic.AllowedValues = allowedWorkerEvidenceReviewStatuses()
			diagnostic.Suggestion = "use one of clean, findings, not_run, or blocked; pass is normalized to clean"
		case strings.Contains(reason, "review.matrix.type"):
			diagnostic.Path = "/review/matrix/type"
			diagnostic.AllowedValues = []string{"general", "stateful/concurrent", "subprocess", "persistence"}
			diagnostic.Suggestion = "select the smallest applicable typed risk matrix"
		case strings.Contains(reason, "review.findings"):
			diagnostic.Path = "/review/findings"
			diagnostic.Suggestion = "record each actionable finding once"
		case strings.Contains(reason, "artifact_links["):
			diagnostic.Path = workerEvidenceJSONPointer(strings.TrimSpace(strings.Split(reason, " ")[0]))
			diagnostic.Suggestion = `omit artifact_links unless needed, or use [{"label":"CI","url":"https://example.test/run"}]`
		case strings.Contains(reason, "artifact_links"):
			diagnostic.Path = "/artifact_links"
			diagnostic.Suggestion = `omit artifact_links unless needed, or use [{"label":"CI","url":"https://example.test/run"}]`
		case strings.Contains(reason, "schema"):
			diagnostic.Path = "/schema"
			diagnostic.AllowedValues = []string{WorkerEvidenceSchemaV1}
			diagnostic.Suggestion = fmt.Sprintf("set schema to %q", WorkerEvidenceSchemaV1)
		}
		diagnostics = append(diagnostics, diagnostic)
	}
	return diagnostics
}

func workerEvidenceJSONPointer(field string) string {
	field = strings.TrimSpace(field)
	field = strings.ReplaceAll(field, ".", "/")
	field = strings.ReplaceAll(field, "[", "/")
	field = strings.ReplaceAll(field, "]", "")
	if field == "" {
		return ""
	}
	return "/" + field
}

func workerEvidenceSuggestionForField(field string) string {
	switch field {
	case "schema":
		return fmt.Sprintf("set schema to %q", WorkerEvidenceSchemaV1)
	case "commands_run":
		return `use an array of command strings, for example ["project-check test"]`
	case "key_assertions":
		return `use an array of concise validation assertions, for example ["validation passed"]`
	case "files_changed":
		return `use an array of changed file paths`
	case "review.status":
		return "use one of clean, findings, not_run, or blocked"
	case "review.findings":
		return "use an empty array when there are no review findings"
	case "review.revision":
		return "record the exact candidate revision reviewed"
	case "review.angle":
		return "name the review angle applied"
	case "review.reused_layers":
		return `use ["none"] when no unchanged review layer was reused`
	case "review.matrix.type":
		return "name the selected general, stateful/concurrent, subprocess, or persistence matrix"
	case "review.matrix.covered_cells":
		return "list the risk-matrix cells inspected"
	case "review.matrix.skipped_cells":
		return "use an empty array or objects with cell and reason"
	case "review.extra_pass_reason":
		return "name the explicit high-risk contract requiring more than one worker pass"
	case "risks":
		return `use ["none"] when there are no known risks`
	case "summary":
		return "include a concise closeout summary"
	default:
		return "fill the required worker_evidence.v1 field"
	}
}

func mustWorkerEvidenceJSONRaw(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
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
