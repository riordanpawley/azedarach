package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	WorkflowContextPacketSchema   = "workflow_context.v1"
	WorkflowResultSummarySchema   = "workflow_result_summary.v1"
	WorkflowContextPacketMaxBytes = 8 * 1024
	WorkflowResultSummaryMaxBytes = 16 * 1024
)

type WorkflowRole string

const (
	WorkflowRoleWorker     WorkflowRole = "worker"
	WorkflowRoleReviewer   WorkflowRole = "reviewer"
	WorkflowRoleValidator  WorkflowRole = "validator"
	WorkflowRoleIntegrator WorkflowRole = "integrator"
)

func (r WorkflowRole) Valid() bool {
	switch r {
	case WorkflowRoleWorker, WorkflowRoleReviewer, WorkflowRoleValidator, WorkflowRoleIntegrator:
		return true
	default:
		return false
	}
}

// WorkflowIssueContextRevision identifies the semantic issue inputs supplied to
// a workflow phase. It is independent of runtime lifecycle fields, so starting
// a session does not invalidate an otherwise identical prompt.
func WorkflowIssueContextRevision(task Task) string {
	material := strings.Join([]string{
		task.ID.String(), task.Title, task.Description, task.Design, task.Acceptance, task.Type.String(),
	}, "\x00")
	sum := sha256.Sum256([]byte(material))
	return "issue-context-sha256:" + hex.EncodeToString(sum[:])
}

type WorkflowArtifactReference struct {
	Label     string `json:"label"`
	Reference string `json:"reference"`
	Digest    string `json:"digest,omitempty"`
}

type WorkflowPacketOmission struct {
	Field  string `json:"field"`
	Count  int    `json:"count"`
	Reason string `json:"reason"`
}

type WorkflowPacketProvenance struct {
	IssueID        string `json:"issue_id,omitempty"`
	ScopeID        string `json:"scope_id,omitempty"`
	SourceRevision string `json:"source_revision"`
	SourceHash     string `json:"source_hash"`
}

type WorkflowContextPacket struct {
	Schema             string                      `json:"schema"`
	Role               WorkflowRole                `json:"role"`
	Provenance         WorkflowPacketProvenance    `json:"provenance"`
	Summary            string                      `json:"summary,omitempty"`
	Requirements       []string                    `json:"requirements,omitempty"`
	UnresolvedFindings []string                    `json:"unresolved_findings,omitempty"`
	AffectedInvariants []string                    `json:"affected_invariants,omitempty"`
	ArtifactLinks      []WorkflowArtifactReference `json:"artifact_links,omitempty"`
	Omitted            []WorkflowPacketOmission    `json:"omitted,omitempty"`
}

type WorkflowContextInput struct {
	Role               WorkflowRole
	IssueID            string
	ScopeID            string
	SourceRevision     string
	Summary            string
	Requirements       []string
	UnresolvedFindings []string
	AffectedInvariants []string
	ArtifactLinks      []WorkflowArtifactReference
}

// BuildWorkflowContextPacket creates the canonical semantic handoff shared by
// provider adapters. It deliberately accepts no transcript, notes, or command
// output fields, so those local and potentially sensitive sources cannot leak
// into a workflow prompt by convenience.
func BuildWorkflowContextPacket(input WorkflowContextInput) (WorkflowContextPacket, error) {
	if !input.Role.Valid() {
		return WorkflowContextPacket{}, fmt.Errorf("unsupported workflow role %q", input.Role)
	}
	if workflowUnsafePacketValue(input.IssueID) || workflowUnsafePacketValue(input.ScopeID) || workflowUnsafePacketValue(input.SourceRevision) {
		return WorkflowContextPacket{}, fmt.Errorf("workflow context provenance contains a sensitive or local value")
	}
	issueID := workflowSafeInline(input.IssueID, 200)
	scopeID := workflowSafeInline(input.ScopeID, 200)
	revision := workflowSafeInline(input.SourceRevision, 200)
	if revision == "" || (issueID == "") == (scopeID == "") {
		return WorkflowContextPacket{}, fmt.Errorf("workflow context requires one exact issue or scope identity and a source revision")
	}
	if input.Role != WorkflowRoleValidator && issueID == "" {
		return WorkflowContextPacket{}, fmt.Errorf("%s workflow context requires exact issue provenance", input.Role)
	}
	packet := WorkflowContextPacket{
		Schema: WorkflowContextPacketSchema,
		Role:   input.Role,
		Provenance: WorkflowPacketProvenance{
			IssueID: issueID, ScopeID: scopeID, SourceRevision: revision,
			SourceHash: workflowProvenanceHash(issueID, scopeID, revision),
		},
	}
	omissions := map[string]map[string]int{}
	packet.Summary = workflowSafeValue("summary", input.Summary, 1024, omissions)

	fields := []struct {
		name   string
		values []string
		target *[]string
	}{
		{name: "requirements", values: input.Requirements, target: &packet.Requirements},
		{name: "unresolved_findings", values: input.UnresolvedFindings, target: &packet.UnresolvedFindings},
		{name: "affected_invariants", values: input.AffectedInvariants, target: &packet.AffectedInvariants},
	}
	for _, field := range fields {
		for _, value := range workflowCanonicalValues(field.name, field.values, omissions) {
			*field.target = append(*field.target, value)
		}
	}
	for _, artifact := range workflowCanonicalArtifacts(input.ArtifactLinks, omissions) {
		packet.ArtifactLinks = append(packet.ArtifactLinks, artifact)
	}

	workflowFitContextPacket(&packet, omissions)
	if encoded, err := json.Marshal(packet); err != nil {
		return WorkflowContextPacket{}, err
	} else if len(encoded) > WorkflowContextPacketMaxBytes {
		return WorkflowContextPacket{}, fmt.Errorf("workflow context packet is %d bytes after deterministic compaction", len(encoded))
	}
	return packet, nil
}

func MarshalWorkflowContextPacket(packet WorkflowContextPacket) ([]byte, error) {
	if packet.Schema != WorkflowContextPacketSchema || !packet.Role.Valid() {
		return nil, fmt.Errorf("invalid workflow context packet identity")
	}
	if packet.Provenance.SourceRevision == "" || (packet.Provenance.IssueID == "") == (packet.Provenance.ScopeID == "") {
		return nil, fmt.Errorf("invalid workflow context provenance identity")
	}
	if packet.Provenance.SourceHash != workflowProvenanceHash(packet.Provenance.IssueID, packet.Provenance.ScopeID, packet.Provenance.SourceRevision) {
		return nil, fmt.Errorf("workflow context provenance hash does not match issue and source revision")
	}
	if workflowUnsafePacketValue(packet.Provenance.IssueID) || workflowUnsafePacketValue(packet.Provenance.ScopeID) || workflowUnsafePacketValue(packet.Provenance.SourceRevision) || workflowUnsafePacketValue(packet.Summary) {
		return nil, fmt.Errorf("workflow context contains a sensitive or local value")
	}
	for _, values := range [][]string{packet.Requirements, packet.UnresolvedFindings, packet.AffectedInvariants} {
		for _, value := range values {
			if workflowUnsafePacketValue(value) {
				return nil, fmt.Errorf("workflow context contains a sensitive or local value")
			}
		}
	}
	for _, artifact := range packet.ArtifactLinks {
		if artifact.Label == "" || workflowUnsafePacketValue(artifact.Label) || !workflowArtifactReferenceAllowed(artifact.Reference) || workflowUnsafePacketValue(artifact.Digest) {
			return nil, fmt.Errorf("workflow context contains an invalid artifact reference")
		}
	}
	encoded, err := json.Marshal(packet)
	if err != nil {
		return nil, err
	}
	if len(encoded) > WorkflowContextPacketMaxBytes {
		return nil, fmt.Errorf("workflow context packet exceeds %d bytes", WorkflowContextPacketMaxBytes)
	}
	return encoded, nil
}

type WorkflowResultSummary struct {
	Schema          string                      `json:"schema"`
	Role            WorkflowRole                `json:"role"`
	Provenance      WorkflowPacketProvenance    `json:"provenance"`
	Status          string                      `json:"status"`
	Outcome         string                      `json:"outcome,omitempty"`
	FailureSummary  string                      `json:"failure_summary,omitempty"`
	ArtifactLinks   []WorkflowArtifactReference `json:"artifact_links,omitempty"`
	OutputRetention string                      `json:"output_retention"`
	Omitted         []WorkflowPacketOmission    `json:"omitted,omitempty"`
}

type WorkflowResultInput struct {
	Role           WorkflowRole
	IssueID        string
	ScopeID        string
	SourceRevision string
	Status         string
	Outcome        string
	FailureSummary string
	ArtifactLinks  []WorkflowArtifactReference
	OutputPresent  bool
}

func BuildWorkflowResultSummary(input WorkflowResultInput) (WorkflowResultSummary, error) {
	contextPacket, err := BuildWorkflowContextPacket(WorkflowContextInput{Role: input.Role, IssueID: input.IssueID, ScopeID: input.ScopeID, SourceRevision: input.SourceRevision})
	if err != nil {
		return WorkflowResultSummary{}, err
	}
	omissions := map[string]map[string]int{}
	result := WorkflowResultSummary{
		Schema: WorkflowResultSummarySchema, Role: input.Role, Provenance: contextPacket.Provenance,
		Status:          workflowSafeValue("status", input.Status, 120, omissions),
		Outcome:         workflowSafeValue("outcome", input.Outcome, 2048, omissions),
		FailureSummary:  workflowSafeValue("failure_summary", input.FailureSummary, 8192, omissions),
		ArtifactLinks:   workflowCanonicalArtifacts(input.ArtifactLinks, omissions),
		OutputRetention: "not_provided",
	}
	if result.Status == "" {
		return WorkflowResultSummary{}, fmt.Errorf("workflow result summary requires a non-sensitive status")
	}
	if input.OutputPresent && len(result.ArtifactLinks) == 0 {
		result.OutputRetention = "unavailable"
	} else if input.OutputPresent {
		result.OutputRetention = "retained"
	}
	workflowFitResultSummary(&result, omissions)
	encoded, err := json.Marshal(result)
	if err != nil {
		return WorkflowResultSummary{}, err
	}
	if len(encoded) > WorkflowResultSummaryMaxBytes {
		return WorkflowResultSummary{}, fmt.Errorf("workflow result summary is %d bytes after deterministic compaction", len(encoded))
	}
	return result, nil
}

func workflowProvenanceHash(issueID, scopeID, revision string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(issueID) + "\x00" + strings.TrimSpace(scopeID) + "\x00" + strings.TrimSpace(revision)))
	return hex.EncodeToString(sum[:])
}

func workflowCanonicalValues(field string, values []string, omissions map[string]map[string]int) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = workflowSafeValue(field, value, 1200, omissions)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func workflowCanonicalArtifacts(values []WorkflowArtifactReference, omissions map[string]map[string]int) []WorkflowArtifactReference {
	seen := map[string]struct{}{}
	out := make([]WorkflowArtifactReference, 0, len(values))
	for _, value := range values {
		if workflowUnsafePacketValue(value.Label) || workflowUnsafePacketValue(value.Digest) || len(strings.TrimSpace(value.Reference)) > 2048 {
			workflowOmit(omissions, "artifact_links", "sensitive_local_or_oversized_reference", 1)
			continue
		}
		value.Label = workflowSafeInline(value.Label, 160)
		value.Reference = strings.TrimSpace(value.Reference)
		value.Digest = workflowSafeInline(value.Digest, 160)
		if value.Label == "" || !workflowArtifactReferenceAllowed(value.Reference) {
			workflowOmit(omissions, "artifact_links", "sensitive_or_local_reference", 1)
			continue
		}
		key := value.Label + "\x00" + value.Reference + "\x00" + value.Digest
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Label == out[j].Label {
			return out[i].Reference < out[j].Reference
		}
		return out[i].Label < out[j].Label
	})
	return out
}

func workflowArtifactReferenceAllowed(value string) bool {
	if value == "" || workflowSensitive(value) || filepath.IsAbs(value) || strings.Contains(value, `:\`) {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	if parsed.Scheme == "" {
		return !strings.Contains(value, "..")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https", "http", "artifact", "validation":
		return true
	default:
		return false
	}
}

func workflowSafeValue(field, value string, max int, omissions map[string]map[string]int) string {
	value = workflowSafeInline(value, 0)
	if value == "" {
		return ""
	}
	if workflowSensitive(value) || workflowContainsLocalPath(value) {
		workflowOmit(omissions, field, "sensitive_or_local_value", 1)
		return ""
	}
	if len(value) <= max {
		return value
	}
	workflowOmit(omissions, field, "value_byte_limit", 1)
	return workflowTruncateBytes(value, max)
}

func workflowSafeInline(value string, max int) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if max > 0 && len(value) > max {
		return workflowTruncateBytes(value, max)
	}
	return value
}

func workflowTruncateBytes(value string, max int) string {
	if max < 4 || len(value) <= max {
		return value
	}
	cut := max - 3
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return strings.TrimSpace(value[:cut]) + "..."
}

func workflowSensitive(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"begin private key", "authorization: bearer", "bearer ", "password=", "passwd=", "token=", "secret=", "client_secret", "api_key=", "api-key:", "private_key", "aws_secret_access_key", "ghp_", "github_pat_", "sk-proj-"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func workflowUnsafePacketValue(value string) bool {
	return value != "" && (workflowSensitive(value) || workflowContainsLocalPath(value))
}

func workflowContainsLocalPath(value string) bool {
	lower := strings.ToLower(value)
	if strings.Contains(value, "/Users/") || strings.Contains(value, "/home/") || strings.Contains(value, "/tmp/") || strings.Contains(value, "/var/folders/") || strings.Contains(lower, `c:\users\`) {
		return true
	}
	for _, field := range strings.Fields(value) {
		candidate := strings.Trim(field, "`'\"()[]{}.,;:")
		if filepath.IsAbs(candidate) {
			return true
		}
	}
	return false
}

func workflowFitContextPacket(packet *WorkflowContextPacket, omissions map[string]map[string]int) {
	for {
		packet.Omitted = workflowOmissions(omissions)
		encoded, _ := json.Marshal(packet)
		if len(encoded) <= WorkflowContextPacketMaxBytes {
			return
		}
		field, ok := workflowRemoveContextTail(packet)
		if !ok {
			return
		}
		workflowOmit(omissions, field, "packet_byte_limit", 1)
	}
}

func workflowRemoveContextTail(packet *WorkflowContextPacket) (string, bool) {
	if n := len(packet.ArtifactLinks); n > 1 {
		packet.ArtifactLinks = packet.ArtifactLinks[:n-1]
		return "artifact_links", true
	}
	if n := len(packet.AffectedInvariants); n > 0 {
		packet.AffectedInvariants = packet.AffectedInvariants[:n-1]
		return "affected_invariants", true
	}
	if n := len(packet.UnresolvedFindings); n > 0 {
		packet.UnresolvedFindings = packet.UnresolvedFindings[:n-1]
		return "unresolved_findings", true
	}
	if n := len(packet.Requirements); n > 0 {
		packet.Requirements = packet.Requirements[:n-1]
		return "requirements", true
	}
	if packet.Summary != "" {
		packet.Summary = ""
		return "summary", true
	}
	if len(packet.ArtifactLinks) == 1 {
		packet.ArtifactLinks = nil
		return "artifact_links", true
	}
	return "", false
}

func workflowFitResultSummary(result *WorkflowResultSummary, omissions map[string]map[string]int) {
	for {
		result.Omitted = workflowOmissions(omissions)
		encoded, _ := json.Marshal(result)
		if len(encoded) <= WorkflowResultSummaryMaxBytes {
			return
		}
		if result.FailureSummary != "" {
			result.FailureSummary = ""
			workflowOmit(omissions, "failure_summary", "summary_byte_limit", 1)
			continue
		}
		if result.Outcome != "" {
			result.Outcome = ""
			workflowOmit(omissions, "outcome", "summary_byte_limit", 1)
			continue
		}
		if n := len(result.ArtifactLinks); n > 1 {
			result.ArtifactLinks = result.ArtifactLinks[:n-1]
			workflowOmit(omissions, "artifact_links", "summary_byte_limit", 1)
			continue
		}
		return
	}
}

func workflowOmit(omissions map[string]map[string]int, field, reason string, count int) {
	if count <= 0 {
		return
	}
	if omissions[field] == nil {
		omissions[field] = map[string]int{}
	}
	omissions[field][reason] += count
}

func workflowOmissions(values map[string]map[string]int) []WorkflowPacketOmission {
	out := make([]WorkflowPacketOmission, 0)
	for field, reasons := range values {
		for reason, count := range reasons {
			out = append(out, WorkflowPacketOmission{Field: field, Count: count, Reason: reason})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Field == out[j].Field {
			return out[i].Reason < out[j].Reason
		}
		return out[i].Field < out[j].Field
	})
	return out
}
