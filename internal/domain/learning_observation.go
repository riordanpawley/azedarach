package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

const (
	LearningObservationSummaryRunes       = 240
	LearningObservationEvidenceRunes      = 8000
	LearningObservationContextBytes       = 16384
	LearningObservationDuplicateHintLimit = 10
)

type LearningSensitivity string

const (
	LearningSensitivityPublic  LearningSensitivity = "public"
	LearningSensitivityPrivate LearningSensitivity = "private"
)

func (s LearningSensitivity) Valid() bool {
	return s == LearningSensitivityPublic || s == LearningSensitivityPrivate
}

// LearningObservationDuplicateHints applies the domain's bounded, stable hint
// semantics to store-selected public candidates. Private captures never hint.
func LearningObservationDuplicateHints(decision LearningCaptureDecision, candidateIDs []string) []string {
	if !decision.PublicProjection || decision.SafeFingerprint == "" {
		return nil
	}
	out := make([]string, 0, min(len(candidateIDs), LearningObservationDuplicateHintLimit))
	seen := make(map[string]struct{}, len(candidateIDs))
	for _, id := range candidateIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if len(out) == LearningObservationDuplicateHintLimit {
			break
		}
	}
	return out
}

type LearningObservationProvenance struct{ Source, Actor, Ref string }

// LearningCaptureInput is the untrusted capture shape presented to the domain.
type LearningCaptureInput struct {
	ProjectID, ObservedBehavior, PreferredBehavior, Outcome, Impact string
	Context                                                         map[string]string
	Provenance                                                      LearningObservationProvenance
	Sensitivity                                                     LearningSensitivity
	Tags, Files                                                     []string
}

// LearningCaptureDecision is the complete normalized policy decision persisted by adapters.
// PublicProjection controls all non-explicit protocol and telemetry surfaces.
type LearningCaptureDecision struct {
	ProjectID, ObservedBehavior, PreferredBehavior, Outcome, Impact string
	Context                                                         map[string]string
	Provenance                                                      LearningObservationProvenance
	Sensitivity                                                     LearningSensitivity
	Summary, SafeFingerprint                                        string
	Tags, Files                                                     []string
	PublicProjection                                                bool
}

// LegacyLearningCapture preserves az learn add input semantics while routing it
// through the same capture policy as typed observations.
func LegacyLearningCapture(projectID, summary, evidence string, private bool, tags, files []string) LearningCaptureInput {
	sensitivity := LearningSensitivityPublic
	if private {
		sensitivity = LearningSensitivityPrivate
	}
	if strings.TrimSpace(summary) == "" {
		summary = deriveLearningSummary(evidence)
	}
	return LearningCaptureInput{ProjectID: projectID, ObservedBehavior: evidence, PreferredBehavior: summary,
		Provenance: LearningObservationProvenance{Source: "az.learn.add"}, Sensitivity: sensitivity, Tags: tags, Files: files}
}

func NormalizeLearningCapture(in LearningCaptureInput) (LearningCaptureDecision, error) {
	d := LearningCaptureDecision{ProjectID: strings.TrimSpace(in.ProjectID), ObservedBehavior: strings.TrimSpace(in.ObservedBehavior),
		PreferredBehavior: strings.TrimSpace(in.PreferredBehavior), Outcome: strings.TrimSpace(in.Outcome), Impact: strings.TrimSpace(in.Impact),
		Context: normalizeCaptureContext(in.Context), Provenance: LearningObservationProvenance{Source: strings.TrimSpace(in.Provenance.Source), Actor: strings.TrimSpace(in.Provenance.Actor), Ref: strings.TrimSpace(in.Provenance.Ref)}, Sensitivity: in.Sensitivity}
	if d.ProjectID == "" || d.ObservedBehavior == "" || d.PreferredBehavior == "" || d.Provenance.Source == "" || !d.Sensitivity.Valid() {
		return d, fmt.Errorf("project, observed behavior, preferred behavior, provenance source, and valid sensitivity are required")
	}
	for label, value := range map[string]string{"observed behavior": d.ObservedBehavior, "preferred behavior": d.PreferredBehavior, "outcome": d.Outcome, "impact": d.Impact, "provenance source": d.Provenance.Source, "provenance actor": d.Provenance.Actor, "provenance ref": d.Provenance.Ref} {
		if hasUnsafeCaptureControl(value) {
			return d, fmt.Errorf("%s contains disallowed control characters", label)
		}
		limit := LearningObservationEvidenceRunes
		if label == "preferred behavior" {
			limit = LearningObservationSummaryRunes
		}
		if len([]rune(value)) > limit {
			return d, fmt.Errorf("%s exceeds %d rune limit", label, limit)
		}
	}
	contextJSON, err := json.Marshal(d.Context)
	if err != nil {
		return d, fmt.Errorf("encode observation context: %w", err)
	}
	if len(contextJSON) > LearningObservationContextBytes {
		return d, fmt.Errorf("observation context exceeds %d byte limit", LearningObservationContextBytes)
	}
	d.PublicProjection = d.Sensitivity == LearningSensitivityPublic
	if !d.PublicProjection {
		d.Summary = "Private learning observation"
		// Tags, files, and fingerprints are correlation indexes. Structured context
		// remains part of the sensitive observation payload and is never placed in
		// the public projection by the daemon adapter.
		d.Tags, d.Files, d.SafeFingerprint = nil, nil, ""
		return d, nil
	}
	d.Summary, d.Tags, d.Files = d.PreferredBehavior, normalizeCaptureSlice(in.Tags), normalizeCaptureSlice(in.Files)
	d.SafeFingerprint, err = LearningObservationFingerprint(d.Sensitivity, d.PreferredBehavior, d.Context)
	return d, err
}

func deriveLearningSummary(evidence string) string {
	s := strings.Join(strings.Fields(evidence), " ")
	r := []rune(s)
	if len(r) > 120 {
		s = strings.TrimSpace(string(r[:120]))
	}
	return s
}

func normalizeCaptureSlice(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func normalizeCaptureContext(context map[string]string) map[string]string {
	if len(context) == 0 {
		return nil
	}
	out := make(map[string]string, len(context))
	for key, value := range context {
		if key = strings.TrimSpace(key); key != "" {
			out[key] = strings.TrimSpace(value)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func hasUnsafeCaptureControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' {
			return true
		}
	}
	return false
}

// LearningObservationFingerprint derives a stable, evidence-free duplicate key.
func LearningObservationFingerprint(sensitivity LearningSensitivity, preferred string, context map[string]string) (string, error) {
	if !sensitivity.Valid() {
		return "", fmt.Errorf("invalid learning sensitivity %q", sensitivity)
	}
	if sensitivity == LearningSensitivityPrivate {
		return "", nil
	}
	preferred = strings.Join(strings.Fields(strings.ToLower(preferred)), " ")
	if preferred == "" {
		return "", fmt.Errorf("preferred behavior is required")
	}
	b, err := json.Marshal(struct {
		Preferred string            `json:"preferred"`
		Context   map[string]string `json:"context"`
	}{preferred, normalizeCaptureContext(context)})
	if err != nil {
		return "", fmt.Errorf("encode learning observation fingerprint: %w", err)
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
