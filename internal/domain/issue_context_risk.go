package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

type IssueContextRiskLevel string

const (
	IssueContextRiskNone   IssueContextRiskLevel = "none"
	IssueContextRiskFYI    IssueContextRiskLevel = "fyi"
	IssueContextRiskMedium IssueContextRiskLevel = "medium"
	IssueContextRiskHigh   IssueContextRiskLevel = "high"
)

type IssueContextRiskEvidence struct {
	IssueID                 string    `json:"issue_id"`
	Relationship            string    `json:"relationship,omitempty"`
	Files                   []string  `json:"files,omitempty"`
	Symbols                 []string  `json:"symbols,omitempty"`
	Tests                   []string  `json:"tests,omitempty"`
	RelatedConsumersAudited []string  `json:"related_consumers_audited,omitempty"`
	RootCause               string    `json:"root_cause,omitempty"`
	Invariant               string    `json:"invariant,omitempty"`
	Validation              string    `json:"validation,omitempty"`
	RiskNotes               []string  `json:"risk_notes,omitempty"`
	EvidenceKinds           []string  `json:"evidence_kinds,omitempty"`
	ObservedAt              time.Time `json:"observed_at,omitempty,omitzero"`
}

type IssueContextRiskInput struct {
	Target        IssueContextRiskEvidence
	ParentIssueID string
	Candidates    []IssueContextRiskEvidence
	GeneratedAt   time.Time
	Since         time.Time
}

type IssueContextRiskPacket struct {
	IssueID           string                     `json:"issue_id"`
	ParentIssueID     string                     `json:"parent_issue_id,omitempty"`
	Level             IssueContextRiskLevel      `json:"level"`
	Confidence        int                        `json:"confidence"`
	Since             time.Time                  `json:"since,omitempty,omitzero"`
	GeneratedAt       time.Time                  `json:"generated_at,omitempty,omitzero"`
	CandidateCount    int                        `json:"candidate_count"`
	OverlapIssueCount int                        `json:"overlap_issue_count"`
	RelatedIssueIDs   []string                   `json:"related_issue_ids,omitempty"`
	Clusters          []IssueContextRiskCluster  `json:"clusters,omitempty"`
	Signals           []string                   `json:"signals,omitempty"`
	CloseoutPrompts   []string                   `json:"closeout_prompts,omitempty"`
	HandoffFields     IssueContextHandoffFields  `json:"handoff_fields"`
	Degraded          bool                       `json:"degraded,omitempty"`
	Timeout           bool                       `json:"timeout,omitempty"`
	DegradedReason    string                     `json:"degraded_reason,omitempty"`
	EvidenceTruncated bool                       `json:"evidence_truncated,omitempty"`
	Evidence          []IssueContextRiskEvidence `json:"evidence,omitempty"`
}

type IssueContextRiskCluster struct {
	Kind   string   `json:"kind"`
	Value  string   `json:"value"`
	Issues []string `json:"issues"`
}

type IssueContextHandoffFields struct {
	StructuredFields []string `json:"structured_fields"`
	Present          []string `json:"present,omitempty"`
	Missing          []string `json:"missing,omitempty"`
}

type IssueContextRiskSummary struct {
	IssueID           string                    `json:"issue_id"`
	ParentIssueID     string                    `json:"parent_issue_id,omitempty"`
	Level             IssueContextRiskLevel     `json:"level"`
	Confidence        int                       `json:"confidence"`
	Since             time.Time                 `json:"since,omitempty,omitzero"`
	GeneratedAt       time.Time                 `json:"generated_at,omitempty,omitzero"`
	CandidateCount    int                       `json:"candidate_count"`
	OverlapIssueCount int                       `json:"overlap_issue_count"`
	RelatedIssueIDs   []string                  `json:"related_issue_ids,omitempty"`
	Signals           []string                  `json:"signals,omitempty"`
	CloseoutPrompts   []string                  `json:"closeout_prompts,omitempty"`
	HandoffFields     IssueContextHandoffFields `json:"handoff_fields"`
	EvidenceSnippets  []IssueContextRiskSnippet `json:"evidence_snippets,omitempty"`
	Degraded          bool                      `json:"degraded,omitempty"`
	Timeout           bool                      `json:"timeout,omitempty"`
	DegradedReason    string                    `json:"degraded_reason,omitempty"`
}

type IssueContextRiskSnippet struct {
	IssueID      string   `json:"issue_id"`
	Relationship string   `json:"relationship,omitempty"`
	Signals      []string `json:"signals,omitempty"`
}

func BuildIssueContextRisk(input IssueContextRiskInput) IssueContextRiskPacket {
	target := normalizeRiskEvidence(input.Target)
	packet := IssueContextRiskPacket{
		IssueID:         target.IssueID,
		ParentIssueID:   strings.TrimSpace(input.ParentIssueID),
		Level:           IssueContextRiskNone,
		Since:           input.Since,
		GeneratedAt:     input.GeneratedAt,
		CandidateCount:  len(input.Candidates),
		HandoffFields:   issueContextHandoffFields(target),
		CloseoutPrompts: []string{},
	}

	candidates := make([]IssueContextRiskEvidence, 0, len(input.Candidates))
	seenCandidates := map[string]struct{}{}
	for _, candidate := range input.Candidates {
		candidate = normalizeRiskEvidence(candidate)
		if candidate.IssueID == "" || candidate.IssueID == target.IssueID {
			continue
		}
		if _, ok := seenCandidates[candidate.IssueID]; ok {
			continue
		}
		seenCandidates[candidate.IssueID] = struct{}{}
		candidates = append(candidates, candidate)
	}

	clusters := append(issueContextRiskClusters("file", target.Files, candidates), issueContextRiskClusters("symbol", target.Symbols, candidates)...)
	clusters = append(clusters, issueContextRiskClusters("test", target.Tests, candidates)...)
	packet.Clusters = clusters
	packet.OverlapIssueCount = countClusterIssues(clusters)
	packet.RelatedIssueIDs = issueContextRiskRelatedIssueIDs(clusters)

	if len(clusters) == 0 {
		if len(target.Files)+len(target.Symbols)+len(target.Tests) == 0 {
			packet.Signals = append(packet.Signals, "no structured locality evidence on target issue")
		} else {
			packet.Signals = append(packet.Signals, "no recent sibling or related issue shares target locality evidence")
		}
		packet.CloseoutPrompts = []string{"Record files_changed plus any root_cause, invariant, changed_symbols, tests_changed, and regression_validation fields if this issue is closing out."}
		return packet
	}

	packet.Evidence = issueContextRiskEvidenceForClusters(target, candidates, clusters)
	score := issueContextRiskScore(clusters, packet.Evidence)
	packet.Confidence = score
	packet.Level = issueContextRiskLevelForScore(score)
	packet.Signals = issueContextRiskSignals(clusters, packet.Evidence)
	packet.CloseoutPrompts = issueContextRiskPrompts(packet.Level)
	return packet
}

func SummarizeIssueContextRisk(packet IssueContextRiskPacket) IssueContextRiskSummary {
	return IssueContextRiskSummary{
		IssueID:           packet.IssueID,
		ParentIssueID:     packet.ParentIssueID,
		Level:             packet.Level,
		Confidence:        packet.Confidence,
		Since:             packet.Since,
		GeneratedAt:       packet.GeneratedAt,
		CandidateCount:    packet.CandidateCount,
		OverlapIssueCount: packet.OverlapIssueCount,
		RelatedIssueIDs:   limitStrings(packet.RelatedIssueIDs, 25),
		Signals:           limitStrings(packet.Signals, 5),
		CloseoutPrompts:   append([]string(nil), packet.CloseoutPrompts...),
		HandoffFields:     packet.HandoffFields,
		EvidenceSnippets:  issueContextRiskSnippets(packet.Evidence, 3),
		Degraded:          packet.Degraded,
		Timeout:           packet.Timeout,
		DegradedReason:    strings.TrimSpace(packet.DegradedReason),
	}
}

func CompactIssueContextRisk(packet IssueContextRiskPacket) IssueContextRiskPacket {
	packet.Signals = limitStrings(packet.Signals, 5)
	packet.RelatedIssueIDs = limitStrings(packet.RelatedIssueIDs, 25)
	packet.Clusters = compactIssueContextRiskClusters(packet.Clusters, 3)
	compactEvidence := compactIssueContextRiskEvidence(packet.Evidence, 3, packet.IssueID)
	packet.EvidenceTruncated = packet.EvidenceTruncated || len(compactEvidence) < len(packet.Evidence)
	packet.Evidence = compactEvidence
	return packet
}

func normalizeRiskEvidence(e IssueContextRiskEvidence) IssueContextRiskEvidence {
	e.IssueID = strings.TrimSpace(e.IssueID)
	e.Relationship = strings.TrimSpace(e.Relationship)
	e.Files = normalizeEvidenceValues(e.Files)
	e.Symbols = normalizeEvidenceValues(e.Symbols)
	e.Tests = normalizeEvidenceValues(e.Tests)
	e.RelatedConsumersAudited = normalizeEvidenceValues(e.RelatedConsumersAudited)
	e.RootCause = strings.TrimSpace(e.RootCause)
	e.Invariant = strings.TrimSpace(e.Invariant)
	e.Validation = strings.TrimSpace(e.Validation)
	e.RiskNotes = normalizeEvidenceValues(e.RiskNotes)
	e.EvidenceKinds = normalizeEvidenceValues(e.EvidenceKinds)
	return e
}

func issueContextHandoffFields(target IssueContextRiskEvidence) IssueContextHandoffFields {
	fields := []string{"files_changed", "root_cause", "invariant", "changed_symbols", "tests_changed", "related_consumers_audited", "regression_validation"}
	presentSet := map[string]bool{}
	if len(target.Files) > 0 {
		presentSet["files_changed"] = true
	}
	if target.RootCause != "" {
		presentSet["root_cause"] = true
	}
	if target.Invariant != "" {
		presentSet["invariant"] = true
	}
	if len(target.Symbols) > 0 {
		presentSet["changed_symbols"] = true
	}
	if len(target.Tests) > 0 {
		presentSet["tests_changed"] = true
	}
	if len(target.RelatedConsumersAudited) > 0 {
		presentSet["related_consumers_audited"] = true
	}
	if target.Validation != "" {
		presentSet["regression_validation"] = true
	}
	var present, missing []string
	for _, field := range fields {
		if presentSet[field] {
			present = append(present, field)
		} else {
			missing = append(missing, field)
		}
	}
	return IssueContextHandoffFields{StructuredFields: fields, Present: present, Missing: missing}
}

func issueContextRiskClusters(kind string, targetValues []string, candidates []IssueContextRiskEvidence) []IssueContextRiskCluster {
	targetSet := make(map[string]struct{}, len(targetValues))
	for _, value := range targetValues {
		targetSet[value] = struct{}{}
	}
	if len(targetSet) == 0 {
		return nil
	}
	issuesByValue := map[string][]string{}
	for _, candidate := range candidates {
		values := candidate.Files
		switch kind {
		case "symbol":
			values = candidate.Symbols
		case "test":
			values = candidate.Tests
		}
		for _, value := range values {
			if _, ok := targetSet[value]; ok {
				issuesByValue[value] = appendUniqueString(issuesByValue[value], candidate.IssueID)
			}
		}
	}
	clusters := make([]IssueContextRiskCluster, 0, len(issuesByValue))
	for value, issues := range issuesByValue {
		sort.Strings(issues)
		clusters = append(clusters, IssueContextRiskCluster{Kind: kind, Value: value, Issues: issues})
	}
	sort.SliceStable(clusters, func(i, j int) bool {
		if len(clusters[i].Issues) == len(clusters[j].Issues) {
			if clusters[i].Kind == clusters[j].Kind {
				return clusters[i].Value < clusters[j].Value
			}
			return clusters[i].Kind < clusters[j].Kind
		}
		return len(clusters[i].Issues) > len(clusters[j].Issues)
	})
	return clusters
}

func issueContextRiskScore(clusters []IssueContextRiskCluster, evidence []IssueContextRiskEvidence) int {
	if len(clusters) == 0 {
		return 0
	}
	score := 25
	overlapIssues := countClusterIssues(clusters)
	if overlapIssues > 1 {
		score += (overlapIssues - 1) * 20
	}
	if len(clusters) > 1 {
		score += min((len(clusters)-1)*10, 20)
	}
	for _, cluster := range clusters {
		switch cluster.Kind {
		case "symbol", "test":
			score += 10
		}
	}
	for _, item := range evidence {
		if len(item.RiskNotes) > 0 || item.RootCause != "" || item.Invariant != "" {
			score += 10
		}
		for _, kind := range item.EvidenceKinds {
			lower := strings.ToLower(kind)
			if strings.Contains(lower, "risk") || strings.Contains(lower, "validation.failed") || strings.Contains(lower, "validation_failed") || strings.Contains(lower, "review_findings") {
				score += 20
				break
			}
		}
	}
	if score > 95 {
		score = 95
	}
	return score
}

func issueContextRiskLevelForScore(score int) IssueContextRiskLevel {
	switch {
	case score >= 70:
		return IssueContextRiskHigh
	case score >= 40:
		return IssueContextRiskMedium
	case score > 0:
		return IssueContextRiskFYI
	default:
		return IssueContextRiskNone
	}
}

func issueContextRiskSignals(clusters []IssueContextRiskCluster, evidence []IssueContextRiskEvidence) []string {
	signals := make([]string, 0, len(clusters)+2)
	for _, cluster := range clusters {
		signals = append(signals, fmt.Sprintf("%s overlap %q with %s", cluster.Kind, cluster.Value, strings.Join(cluster.Issues, ", ")))
		if len(signals) >= 5 {
			break
		}
	}
	for _, item := range evidence {
		if len(item.RiskNotes) > 0 {
			signals = append(signals, fmt.Sprintf("%s has recorded risk evidence", item.IssueID))
			break
		}
	}
	return signals
}

func issueContextRiskPrompts(level IssueContextRiskLevel) []string {
	switch level {
	case IssueContextRiskMedium:
		return []string{
			"What invariant was added or preserved for the repeated local area?",
			"Which related consumers were audited?",
			"What regression test or validation proves this repeated failure will not recur?",
		}
	case IssueContextRiskHigh:
		return []string{
			"Record a diagnosis or structured risk note before marking this issue ready for closeout.",
			"What invariant was added or preserved for the repeated local area?",
			"Which related consumers were audited?",
			"What regression test or validation proves this repeated failure will not recur?",
		}
	case IssueContextRiskFYI:
		return []string{"Review the overlap during closeout; no extra gate is suggested from this packet alone."}
	default:
		return nil
	}
}

func IssueContextRiskRequiresStructuredCloseout(packet IssueContextRiskPacket) bool {
	if packet.Level != IssueContextRiskHigh {
		return false
	}
	for _, evidence := range packet.Evidence {
		if !naming.IssueIDsEqual(evidence.IssueID, packet.IssueID) {
			continue
		}
		return evidence.RootCause == "" && evidence.Invariant == "" && evidence.Validation == "" && len(evidence.RiskNotes) == 0
	}
	return true
}

func issueContextRiskEvidenceForClusters(target IssueContextRiskEvidence, candidates []IssueContextRiskEvidence, clusters []IssueContextRiskCluster) []IssueContextRiskEvidence {
	include := map[string]bool{target.IssueID: true}
	for _, cluster := range clusters {
		for _, issueID := range cluster.Issues {
			include[issueID] = true
		}
	}
	out := []IssueContextRiskEvidence{target}
	for _, candidate := range candidates {
		if include[candidate.IssueID] {
			out = append(out, candidate)
		}
	}
	sort.SliceStable(out[1:], func(i, j int) bool {
		left, right := out[i+1], out[j+1]
		if left.ObservedAt.Equal(right.ObservedAt) {
			return left.IssueID < right.IssueID
		}
		return left.ObservedAt.After(right.ObservedAt)
	})
	return out
}

func issueContextRiskRelatedIssueIDs(clusters []IssueContextRiskCluster) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, cluster := range clusters {
		for _, issueID := range cluster.Issues {
			if _, ok := seen[issueID]; ok {
				continue
			}
			seen[issueID] = struct{}{}
			out = append(out, issueID)
		}
	}
	sort.Strings(out)
	return out
}

func compactIssueContextRiskClusters(clusters []IssueContextRiskCluster, limit int) []IssueContextRiskCluster {
	if limit <= 0 || len(clusters) <= limit {
		return append([]IssueContextRiskCluster(nil), clusters...)
	}
	return append([]IssueContextRiskCluster(nil), clusters[:limit]...)
}

func compactIssueContextRiskEvidence(evidence []IssueContextRiskEvidence, limit int, targetIssueID string) []IssueContextRiskEvidence {
	if limit <= 0 || len(evidence) <= limit {
		return append([]IssueContextRiskEvidence(nil), evidence...)
	}
	out := make([]IssueContextRiskEvidence, 0, limit)
	for _, item := range evidence {
		if naming.IssueIDsEqual(item.IssueID, targetIssueID) {
			out = append(out, item)
			break
		}
	}
	for _, item := range evidence {
		if len(out) >= limit {
			break
		}
		if naming.IssueIDsEqual(item.IssueID, targetIssueID) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func issueContextRiskSnippets(evidence []IssueContextRiskEvidence, limit int) []IssueContextRiskSnippet {
	if limit <= 0 {
		return nil
	}
	snippets := make([]IssueContextRiskSnippet, 0, min(limit, len(evidence)))
	for _, item := range evidence {
		if len(snippets) >= limit {
			break
		}
		signals := issueContextRiskEvidenceSignals(item)
		if len(signals) == 0 {
			continue
		}
		snippets = append(snippets, IssueContextRiskSnippet{
			IssueID:      item.IssueID,
			Relationship: item.Relationship,
			Signals:      signals,
		})
	}
	return snippets
}

func issueContextRiskEvidenceSignals(evidence IssueContextRiskEvidence) []string {
	var signals []string
	if len(evidence.Files) > 0 {
		signals = append(signals, fmt.Sprintf("files: %s", strings.Join(limitStrings(evidence.Files, 3), ", ")))
	}
	if len(evidence.Symbols) > 0 {
		signals = append(signals, fmt.Sprintf("symbols: %s", strings.Join(limitStrings(evidence.Symbols, 3), ", ")))
	}
	if len(evidence.Tests) > 0 {
		signals = append(signals, fmt.Sprintf("tests: %s", strings.Join(limitStrings(evidence.Tests, 3), ", ")))
	}
	if evidence.RootCause != "" {
		signals = append(signals, "root cause recorded")
	}
	if evidence.Invariant != "" {
		signals = append(signals, "invariant recorded")
	}
	if evidence.Validation != "" {
		signals = append(signals, "validation recorded")
	}
	if len(evidence.RiskNotes) > 0 {
		signals = append(signals, fmt.Sprintf("risk: %s", strings.Join(limitStrings(evidence.RiskNotes, 2), "; ")))
	}
	if len(evidence.EvidenceKinds) > 0 {
		signals = append(signals, fmt.Sprintf("evidence: %s", strings.Join(limitStrings(evidence.EvidenceKinds, 3), ", ")))
	}
	return limitStrings(signals, 4)
}

func countClusterIssues(clusters []IssueContextRiskCluster) int {
	seen := map[string]struct{}{}
	for _, cluster := range clusters {
		for _, issueID := range cluster.Issues {
			seen[issueID] = struct{}{}
		}
	}
	return len(seen)
}

func limitStrings(values []string, limit int) []string {
	if limit <= 0 || len(values) == 0 {
		return nil
	}
	if len(values) <= limit {
		return append([]string(nil), values...)
	}
	return append([]string(nil), values[:limit]...)
}

func normalizeEvidenceValues(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.Join(strings.Fields(value), " ")
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func appendUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if naming.IssueIDsEqual(existing, value) || existing == value {
			return values
		}
	}
	return append(values, value)
}
