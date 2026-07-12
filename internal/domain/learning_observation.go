package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type LearningSensitivity string

const (
	LearningSensitivityPublic  LearningSensitivity = "public"
	LearningSensitivityPrivate LearningSensitivity = "private"
)

func (s LearningSensitivity) Valid() bool {
	return s == LearningSensitivityPublic || s == LearningSensitivityPrivate
}

// LearningObservationFingerprint derives a stable, evidence-free duplicate key.
// Private observations deliberately receive no fingerprint because even a digest
// of sensitive behavior can become an unsafe correlation index.
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
	keys := make([]string, 0, len(context))
	for key := range context {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	canonical := make(map[string]string, len(keys))
	for _, key := range keys {
		trimmed := strings.TrimSpace(key)
		if trimmed != "" {
			canonical[trimmed] = strings.TrimSpace(context[key])
		}
	}
	b, err := json.Marshal(struct {
		Preferred string            `json:"preferred"`
		Context   map[string]string `json:"context"`
	}{preferred, canonical})
	if err != nil {
		return "", fmt.Errorf("encode learning observation fingerprint: %w", err)
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
