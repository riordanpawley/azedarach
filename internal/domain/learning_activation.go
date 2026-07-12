package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type LearningActivationOutcome string

const (
	LearningOutcomeHelpful      LearningActivationOutcome = "helpful"
	LearningOutcomeFollowed     LearningActivationOutcome = "followed"
	LearningOutcomeContradicted LearningActivationOutcome = "contradicted"
	LearningOutcomeUnknown      LearningActivationOutcome = "unknown"
)

func (o LearningActivationOutcome) Valid() bool {
	switch o {
	case LearningOutcomeHelpful, LearningOutcomeFollowed, LearningOutcomeContradicted, LearningOutcomeUnknown:
		return true
	}
	return false
}

type LearningOutcomeSource string

const (
	LearningOutcomeExplicit LearningOutcomeSource = "explicit"
	LearningOutcomeHuman    LearningOutcomeSource = "human"
	LearningOutcomeAgent    LearningOutcomeSource = "agent"
	LearningOutcomeInferred LearningOutcomeSource = "inferred"
)

func (s LearningOutcomeSource) Valid() bool {
	return s == LearningOutcomeExplicit || s == LearningOutcomeHuman || s == LearningOutcomeAgent || s == LearningOutcomeInferred
}

func (s LearningOutcomeSource) ResolutionPriority() int {
	switch s {
	case LearningOutcomeHuman, LearningOutcomeExplicit:
		return 3
	case LearningOutcomeAgent:
		return 2
	case LearningOutcomeInferred:
		return 1
	default:
		return 0
	}
}

func LearningActivationProposalExpiry(now time.Time) time.Time { return now.UTC().Add(-24 * time.Hour) }

// LearningContextFingerprint deliberately accepts only structured, non-evidence context.
func LearningContextFingerprint(issueID, requirementID string, tags, files []string) (string, error) {
	canonical := struct {
		Issue       string   `json:"issue,omitempty"`
		Requirement string   `json:"requirement,omitempty"`
		Tags        []string `json:"tags,omitempty"`
		Files       []string `json:"files,omitempty"`
	}{
		Issue: strings.TrimSpace(issueID), Requirement: strings.TrimSpace(requirementID), Tags: canonicalKeys(tags), Files: canonicalKeys(files),
	}
	b, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal activation context: %w", err)
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func LearningActivationContextFingerprint(purpose, sessionID, issueID, requirementID, query string, tags, files []string) (string, error) {
	canonical := struct {
		Purpose, Session, Issue, Requirement, Query string
		Tags, Files                                 []string
	}{strings.TrimSpace(purpose), strings.TrimSpace(sessionID), strings.TrimSpace(issueID), strings.TrimSpace(requirementID), strings.TrimSpace(query), canonicalKeys(tags), canonicalKeys(files)}
	b, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal contextual activation: %w", err)
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func canonicalKeys(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
