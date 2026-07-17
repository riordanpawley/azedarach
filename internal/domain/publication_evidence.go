package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// PublicationEvidenceLayer separates review of an immutable change from proof
// of affected runtime paths and publication of one exact synthetic merge.
type PublicationEvidenceLayer string

const (
	PublicationEvidencePatchReview PublicationEvidenceLayer = "patch_review"
	PublicationEvidenceActivePath  PublicationEvidenceLayer = "active_path"
	PublicationEvidenceMergeResult PublicationEvidenceLayer = "merge_result"
)

func (l PublicationEvidenceLayer) Valid() bool {
	return l == PublicationEvidencePatchReview || l == PublicationEvidenceActivePath || l == PublicationEvidenceMergeResult
}

type PublicationEvidenceCost struct {
	WallMilliseconds int64 `json:"wall_milliseconds,omitempty"`
	CPUMilliseconds  int64 `json:"cpu_milliseconds,omitempty"`
	Tokens           int64 `json:"tokens,omitempty"`
	CacheBytes       int64 `json:"cache_bytes,omitempty"`
}

type PublicationEvidenceCoverage struct {
	Paths        []string `json:"paths,omitempty"`
	Dependencies []string `json:"dependencies,omitempty"`
	Surfaces     []string `json:"surfaces,omitempty"`
}

// PublicationEvidence is immutable provenance for one completed proof. Patch
// identity deliberately does not include BaseRevision: the same patch can stay
// reviewed while an unrelated integration base advances.
type PublicationEvidence struct {
	EvidenceID             string                      `json:"evidence_id"`
	ProjectID              string                      `json:"project_id"`
	IssueID                string                      `json:"issue_id"`
	Layer                  PublicationEvidenceLayer    `json:"layer"`
	PatchDigest            string                      `json:"patch_digest,omitempty"`
	SourceRevision         string                      `json:"source_revision"`
	BaseRevision           string                      `json:"base_revision,omitempty"`
	ResultRevision         string                      `json:"result_revision,omitempty"`
	Producer               string                      `json:"producer"`
	PolicyVersion          string                      `json:"policy_version"`
	EnvironmentFingerprint string                      `json:"environment_fingerprint"`
	ReusedFromEvidenceID   string                      `json:"reused_from_evidence_id,omitempty"`
	Coverage               PublicationEvidenceCoverage `json:"coverage"`
	Cost                   PublicationEvidenceCost     `json:"cost"`
	CreatedAt              time.Time                   `json:"created_at"`
}

func (e PublicationEvidence) Validate() error {
	if strings.TrimSpace(e.EvidenceID) == "" || strings.TrimSpace(e.ProjectID) == "" || strings.TrimSpace(e.IssueID) == "" {
		return fmt.Errorf("publication evidence requires evidence, project, and issue identity")
	}
	if !e.Layer.Valid() {
		return fmt.Errorf("unsupported publication evidence layer %q", e.Layer)
	}
	if strings.TrimSpace(e.SourceRevision) == "" || strings.TrimSpace(e.Producer) == "" || strings.TrimSpace(e.PolicyVersion) == "" || strings.TrimSpace(e.EnvironmentFingerprint) == "" {
		return fmt.Errorf("publication evidence requires source revision, producer, policy version, and environment fingerprint")
	}
	if e.CreatedAt.IsZero() {
		return fmt.Errorf("publication evidence requires creation time")
	}
	if strings.TrimSpace(e.ReusedFromEvidenceID) == strings.TrimSpace(e.EvidenceID) {
		return fmt.Errorf("publication evidence cannot reuse itself")
	}
	if e.Cost.WallMilliseconds < 0 || e.Cost.CPUMilliseconds < 0 || e.Cost.Tokens < 0 || e.Cost.CacheBytes < 0 {
		return fmt.Errorf("publication evidence cost cannot be negative")
	}
	switch e.Layer {
	case PublicationEvidencePatchReview, PublicationEvidenceActivePath:
		if strings.TrimSpace(e.PatchDigest) == "" {
			return fmt.Errorf("%s evidence requires patch digest", e.Layer)
		}
		if strings.TrimSpace(e.ResultRevision) != "" {
			return fmt.Errorf("%s evidence cannot identify a synthetic merge result", e.Layer)
		}
	case PublicationEvidenceMergeResult:
		if strings.TrimSpace(e.BaseRevision) == "" || strings.TrimSpace(e.ResultRevision) == "" {
			return fmt.Errorf("merge_result evidence requires base and result revisions")
		}
	}
	return nil
}

type PublicationInvalidationReason string

const (
	PublicationInvalidSourceChange       PublicationInvalidationReason = "source_changed"
	PublicationInvalidPatchChange        PublicationInvalidationReason = "patch_changed"
	PublicationInvalidDirtyWorktree      PublicationInvalidationReason = "dirty_worktree"
	PublicationInvalidMergeConflict      PublicationInvalidationReason = "merge_conflict"
	PublicationInvalidMaterialDecision   PublicationInvalidationReason = "material_decision_changed"
	PublicationInvalidPathOverlap        PublicationInvalidationReason = "path_overlap"
	PublicationInvalidDependencyOverlap  PublicationInvalidationReason = "dependency_overlap"
	PublicationInvalidHighRiskBaseChange PublicationInvalidationReason = "high_risk_base_changed"
	PublicationInvalidToolchainChange    PublicationInvalidationReason = "toolchain_changed"
	PublicationInvalidPolicyChange       PublicationInvalidationReason = "policy_changed"
	PublicationInvalidEnvironmentChange  PublicationInvalidationReason = "environment_changed"
	PublicationInvalidMergeInputChange   PublicationInvalidationReason = "merge_input_changed"
	PublicationInvalidCapabilityAbsent   PublicationInvalidationReason = "capability_absent"
	PublicationInvalidImpactUnknown      PublicationInvalidationReason = "impact_unknown"
)

func (r PublicationInvalidationReason) Valid() bool {
	switch r {
	case PublicationInvalidSourceChange, PublicationInvalidPatchChange, PublicationInvalidDirtyWorktree,
		PublicationInvalidMergeConflict, PublicationInvalidMaterialDecision, PublicationInvalidPathOverlap,
		PublicationInvalidDependencyOverlap, PublicationInvalidHighRiskBaseChange, PublicationInvalidToolchainChange,
		PublicationInvalidPolicyChange, PublicationInvalidEnvironmentChange, PublicationInvalidMergeInputChange,
		PublicationInvalidCapabilityAbsent, PublicationInvalidImpactUnknown:
		return true
	default:
		return false
	}
}

type PublicationEvidenceInvalidation struct {
	InvalidationID string                        `json:"invalidation_id"`
	EvidenceID     string                        `json:"evidence_id"`
	Reason         PublicationInvalidationReason `json:"reason"`
	Details        string                        `json:"details"`
	CreatedAt      time.Time                     `json:"created_at"`
}

func (i PublicationEvidenceInvalidation) Validate() error {
	if strings.TrimSpace(i.InvalidationID) == "" || strings.TrimSpace(i.EvidenceID) == "" || !i.Reason.Valid() || strings.TrimSpace(i.Details) == "" || i.CreatedAt.IsZero() {
		return fmt.Errorf("publication evidence invalidation requires identity, evidence, reason, details, and creation time")
	}
	return nil
}

// PublicationEvidenceCandidate describes the candidate and intervening base
// movement against which an immutable proof is assessed.
type PublicationEvidenceCandidate struct {
	PatchDigest             string   `json:"patch_digest,omitempty"`
	SourceRevision          string   `json:"source_revision"`
	BaseRevision            string   `json:"base_revision,omitempty"`
	ResultRevision          string   `json:"result_revision,omitempty"`
	PolicyVersion           string   `json:"policy_version"`
	EnvironmentFingerprint  string   `json:"environment_fingerprint"`
	Dirty                   bool     `json:"dirty"`
	Conflict                bool     `json:"conflict"`
	MaterialDecisionChanged bool     `json:"material_decision_changed"`
	ToolchainChanged        bool     `json:"toolchain_changed"`
	ImpactKnown             bool     `json:"impact_known"`
	CapabilityAvailable     bool     `json:"capability_available"`
	ChangedPaths            []string `json:"changed_paths,omitempty"`
	ChangedDependencies     []string `json:"changed_dependencies,omitempty"`
	ChangedSurfaces         []string `json:"changed_surfaces,omitempty"`
}

// PublicationEvidencePolicy is project configuration, not an Azedarach
// toolchain default. ExactBaseSurfaces contains project-owned risk names.
type PublicationEvidencePolicy struct {
	Version                     string   `json:"version"`
	ExactBaseSurfaces           []string `json:"exact_base_surfaces,omitempty"`
	InvalidatePathOverlap       bool     `json:"invalidate_path_overlap"`
	InvalidateDependencyOverlap bool     `json:"invalidate_dependency_overlap"`
	RequireEnvironmentMatch     bool     `json:"require_environment_match"`
	FailClosedUnknownImpact     bool     `json:"fail_closed_unknown_impact"`
	RequireCapability           bool     `json:"require_capability"`
}

func (c PublicationEvidenceCandidate) Validate() error {
	if strings.TrimSpace(c.SourceRevision) == "" || strings.TrimSpace(c.BaseRevision) == "" || strings.TrimSpace(c.PolicyVersion) == "" || strings.TrimSpace(c.EnvironmentFingerprint) == "" {
		return fmt.Errorf("publication evidence candidate requires source revision, base revision, policy version, and environment fingerprint")
	}
	return nil
}

func (p PublicationEvidencePolicy) Validate() error {
	if strings.TrimSpace(p.Version) == "" {
		return fmt.Errorf("publication evidence policy requires version")
	}
	return nil
}

type PublicationEvidenceAssessment struct {
	EvidenceID       string                          `json:"evidence_id"`
	Layer            PublicationEvidenceLayer        `json:"layer"`
	Retained         bool                            `json:"retained"`
	BaseMovementOnly bool                            `json:"base_movement_only,omitempty"`
	Reasons          []PublicationInvalidationReason `json:"reasons,omitempty"`
	Details          []string                        `json:"details,omitempty"`
}

type PublicationEvidenceSnapshot struct {
	Schema        string                            `json:"schema"`
	ProjectID     string                            `json:"project_id"`
	IssueID       string                            `json:"issue_id,omitempty"`
	Evidence      []PublicationEvidence             `json:"evidence"`
	Invalidations []PublicationEvidenceInvalidation `json:"invalidations"`
	Revision      int64                             `json:"revision"`
}

type PublicationEvidenceDiagnostic struct {
	State        string                          `json:"state"`
	Availability string                          `json:"availability"`
	Revision     int64                           `json:"revision,omitempty"`
	PatchReview  int                             `json:"patch_review"`
	ActivePath   int                             `json:"active_path"`
	MergeResult  int                             `json:"merge_result"`
	Invalidated  int                             `json:"invalidated"`
	Reasons      []PublicationInvalidationReason `json:"reasons,omitempty"`
	Detail       string                          `json:"detail,omitempty"`
}

// EvidenceDiagnostic keeps task projections compact while the durable ledger
// retains the more explicit PublicationEvidence naming.
type EvidenceDiagnostic = PublicationEvidenceDiagnostic

func SummarizePublicationEvidence(snapshot PublicationEvidenceSnapshot) PublicationEvidenceDiagnostic {
	diagnostic := PublicationEvidenceDiagnostic{State: "none", Availability: "available", Revision: snapshot.Revision}
	effectiveInvalidations := EffectivePublicationEvidenceInvalidations(snapshot)
	invalidated := make(map[string]struct{}, len(effectiveInvalidations))
	seenReasons := make(map[PublicationInvalidationReason]struct{}, len(effectiveInvalidations))
	for _, invalidation := range effectiveInvalidations {
		invalidated[invalidation.EvidenceID] = struct{}{}
		if _, ok := seenReasons[invalidation.Reason]; !ok {
			diagnostic.Reasons = append(diagnostic.Reasons, invalidation.Reason)
			seenReasons[invalidation.Reason] = struct{}{}
		}
	}
	for _, evidence := range snapshot.Evidence {
		if _, invalid := invalidated[evidence.EvidenceID]; invalid {
			diagnostic.Invalidated++
			continue
		}
		switch evidence.Layer {
		case PublicationEvidencePatchReview:
			diagnostic.PatchReview++
		case PublicationEvidenceActivePath:
			diagnostic.ActivePath++
		case PublicationEvidenceMergeResult:
			diagnostic.MergeResult++
		}
	}
	retained := diagnostic.PatchReview + diagnostic.ActivePath + diagnostic.MergeResult
	switch {
	case retained > 0 && diagnostic.Invalidated > 0:
		diagnostic.State = "partial"
	case retained > 0:
		diagnostic.State = "retained"
	case diagnostic.Invalidated > 0:
		diagnostic.State = "invalidated"
	}
	return diagnostic
}

func EffectivePublicationEvidenceInvalidations(snapshot PublicationEvidenceSnapshot) []PublicationEvidenceInvalidation {
	out := append([]PublicationEvidenceInvalidation(nil), snapshot.Invalidations...)
	invalidByEvidence := make(map[string][]PublicationEvidenceInvalidation)
	for _, invalidation := range snapshot.Invalidations {
		invalidByEvidence[invalidation.EvidenceID] = append(invalidByEvidence[invalidation.EvidenceID], invalidation)
	}
	byID := make(map[string]PublicationEvidence, len(snapshot.Evidence))
	for _, evidence := range snapshot.Evidence {
		byID[evidence.EvidenceID] = evidence
	}
	for _, evidence := range snapshot.Evidence {
		seen := map[string]struct{}{evidence.EvidenceID: {}}
		for sourceID := strings.TrimSpace(evidence.ReusedFromEvidenceID); sourceID != ""; {
			if _, cycle := seen[sourceID]; cycle {
				break
			}
			seen[sourceID] = struct{}{}
			for _, sourceInvalidation := range invalidByEvidence[sourceID] {
				out = append(out, PublicationEvidenceInvalidation{
					InvalidationID: sourceInvalidation.InvalidationID + ":reused:" + evidence.EvidenceID,
					EvidenceID:     evidence.EvidenceID, Reason: sourceInvalidation.Reason,
					Details:   "reused evidence invalidated by " + sourceID + ": " + sourceInvalidation.Details,
					CreatedAt: sourceInvalidation.CreatedAt,
				})
			}
			source, ok := byID[sourceID]
			if !ok {
				break
			}
			sourceID = strings.TrimSpace(source.ReusedFromEvidenceID)
		}
	}
	return out
}

func EvaluatePublicationEvidence(e PublicationEvidence, candidate PublicationEvidenceCandidate, policy PublicationEvidencePolicy) PublicationEvidenceAssessment {
	assessment := PublicationEvidenceAssessment{EvidenceID: e.EvidenceID, Layer: e.Layer, Retained: true}
	add := func(reason PublicationInvalidationReason, detail string) {
		assessment.Retained = false
		assessment.Reasons = append(assessment.Reasons, reason)
		assessment.Details = append(assessment.Details, detail)
	}
	if strings.TrimSpace(candidate.SourceRevision) != strings.TrimSpace(e.SourceRevision) {
		add(PublicationInvalidSourceChange, "candidate source revision differs from evidence")
	}
	if e.Layer != PublicationEvidenceMergeResult && strings.TrimSpace(candidate.PatchDigest) != strings.TrimSpace(e.PatchDigest) {
		add(PublicationInvalidPatchChange, "candidate patch digest differs from evidence")
	}
	if candidate.Dirty {
		add(PublicationInvalidDirtyWorktree, "candidate worktree is dirty")
	}
	if candidate.Conflict {
		add(PublicationInvalidMergeConflict, "candidate has merge conflicts")
	}
	if candidate.MaterialDecisionChanged {
		add(PublicationInvalidMaterialDecision, "material decisions changed after evidence was produced")
	}
	if strings.TrimSpace(candidate.PolicyVersion) != strings.TrimSpace(e.PolicyVersion) || strings.TrimSpace(policy.Version) != strings.TrimSpace(e.PolicyVersion) {
		add(PublicationInvalidPolicyChange, "evidence policy version is not current")
	}
	if policy.RequireEnvironmentMatch && strings.TrimSpace(candidate.EnvironmentFingerprint) != strings.TrimSpace(e.EnvironmentFingerprint) {
		add(PublicationInvalidEnvironmentChange, "environment fingerprint is incompatible")
	}
	if candidate.ToolchainChanged {
		add(PublicationInvalidToolchainChange, "configured toolchain changed")
	}
	if policy.RequireCapability && !candidate.CapabilityAvailable {
		add(PublicationInvalidCapabilityAbsent, "configured impact capability is unavailable")
	}
	if policy.FailClosedUnknownImpact && !candidate.ImpactKnown {
		add(PublicationInvalidImpactUnknown, "intervening-change impact is unknown")
	}
	if policy.InvalidatePathOverlap {
		if overlap := evidencePathOverlap(e.Coverage.Paths, candidate.ChangedPaths); len(overlap) > 0 {
			add(PublicationInvalidPathOverlap, "changed paths overlap evidence coverage: "+strings.Join(overlap, ", "))
		}
	}
	if policy.InvalidateDependencyOverlap {
		if overlap := evidenceOverlap(e.Coverage.Dependencies, candidate.ChangedDependencies); len(overlap) > 0 {
			add(PublicationInvalidDependencyOverlap, "changed dependencies overlap evidence coverage: "+strings.Join(overlap, ", "))
		}
	}
	baseMoved := strings.TrimSpace(e.BaseRevision) != "" && strings.TrimSpace(candidate.BaseRevision) != strings.TrimSpace(e.BaseRevision)
	exactBaseSurfaces := append([]string{"migration", "protocol", "schema", "shared_invariant", "authority_boundary", "generated_artifact"}, policy.ExactBaseSurfaces...)
	if baseMoved && len(evidenceOverlap(exactBaseSurfaces, candidate.ChangedSurfaces)) > 0 {
		add(PublicationInvalidHighRiskBaseChange, "base changed across a configured exact-base surface")
	}
	if e.Layer == PublicationEvidenceMergeResult && (strings.TrimSpace(candidate.BaseRevision) != strings.TrimSpace(e.BaseRevision) || strings.TrimSpace(candidate.ResultRevision) != strings.TrimSpace(e.ResultRevision)) {
		add(PublicationInvalidMergeInputChange, "synthetic merge base or result identity changed")
	}
	assessment.BaseMovementOnly = assessment.Retained && baseMoved
	return assessment
}

func evidencePathOverlap(covered, changed []string) []string {
	var overlap []string
	seen := make(map[string]struct{})
	for _, coveredPath := range covered {
		coveredPath = strings.Trim(strings.TrimSpace(coveredPath), "/")
		if coveredPath == "" {
			continue
		}
		for _, changedPath := range changed {
			changedPath = strings.Trim(strings.TrimSpace(changedPath), "/")
			if changedPath == "" || (coveredPath != changedPath && !strings.HasPrefix(coveredPath, changedPath+"/") && !strings.HasPrefix(changedPath, coveredPath+"/")) {
				continue
			}
			key := coveredPath + " ↔ " + changedPath
			if _, ok := seen[key]; !ok {
				overlap = append(overlap, key)
				seen[key] = struct{}{}
			}
		}
	}
	sort.Strings(overlap)
	return overlap
}

func ApplyPublicationEvidenceInvalidations(assessment PublicationEvidenceAssessment, invalidations []PublicationEvidenceInvalidation) PublicationEvidenceAssessment {
	seen := make(map[PublicationInvalidationReason]struct{}, len(assessment.Reasons))
	for _, reason := range assessment.Reasons {
		seen[reason] = struct{}{}
	}
	for _, invalidation := range invalidations {
		if invalidation.EvidenceID != assessment.EvidenceID {
			continue
		}
		assessment.Retained = false
		assessment.BaseMovementOnly = false
		if _, ok := seen[invalidation.Reason]; !ok {
			assessment.Reasons = append(assessment.Reasons, invalidation.Reason)
			seen[invalidation.Reason] = struct{}{}
		}
		assessment.Details = append(assessment.Details, invalidation.Details)
	}
	return assessment
}

func evidenceOverlap(left, right []string) []string {
	rightSet := make(map[string]struct{}, len(right))
	for _, value := range right {
		if value = strings.TrimSpace(value); value != "" {
			rightSet[value] = struct{}{}
		}
	}
	var overlap []string
	for _, value := range left {
		if value = strings.TrimSpace(value); value != "" {
			if _, ok := rightSet[value]; ok {
				overlap = append(overlap, value)
			}
		}
	}
	sort.Strings(overlap)
	return overlap
}
