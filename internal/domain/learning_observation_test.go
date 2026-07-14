package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeLearningCapturePublicDecision(t *testing.T) {
	d, err := NormalizeLearningCapture(LearningCaptureInput{ProjectID: " proj ", ObservedBehavior: " retried forever ", PreferredBehavior: " use bounded retries ", Context: map[string]string{" surface ": " cli "}, Provenance: LearningObservationProvenance{Source: " hook "}, Sensitivity: LearningSensitivityPublic, Tags: []string{" reliability ", "reliability"}, Files: []string{" retry.go "}})
	require.NoError(t, err)
	require.True(t, d.PublicProjection)
	require.Equal(t, "use bounded retries", d.Summary)
	require.Equal(t, map[string]string{"surface": "cli"}, d.Context)
	require.Equal(t, []string{"reliability"}, d.Tags)
	require.NotEmpty(t, d.SafeFingerprint)
}

func TestNormalizeLearningCapturePrivateRemovesEveryCorrelationSurface(t *testing.T) {
	d, err := NormalizeLearningCapture(LearningCaptureInput{ProjectID: "proj", ObservedBehavior: "token=secret", PreferredBehavior: "never expose secret", Outcome: "secret outcome", Impact: "secret impact", Context: map[string]string{"token": "secret"}, Provenance: LearningObservationProvenance{Source: "hook", Ref: "secret-ref"}, Sensitivity: LearningSensitivityPrivate, Tags: []string{"secret-tag"}, Files: []string{"secret.go"}})
	require.NoError(t, err)
	require.False(t, d.PublicProjection)
	require.Equal(t, "Private learning observation", d.Summary)
	require.Equal(t, map[string]string{"token": "secret"}, d.Context)
	require.Empty(t, d.Tags)
	require.Empty(t, d.Files)
	require.Empty(t, d.SafeFingerprint)
}

func TestLegacyLearningCaptureDerivesCompatibleBoundedSummary(t *testing.T) {
	in := LegacyLearningCapture("proj", "", strings.Repeat("word ", 40), false, nil, nil)
	require.Equal(t, "az.learn.add", in.Provenance.Source)
	require.LessOrEqual(t, len([]rune(in.PreferredBehavior)), 120)
}

func TestNormalizeLearningCaptureRejectsUnsafeMetadata(t *testing.T) {
	_, err := NormalizeLearningCapture(LearningCaptureInput{ProjectID: "proj", ObservedBehavior: "observed", PreferredBehavior: "preferred", Provenance: LearningObservationProvenance{Source: "hook\x00"}, Sensitivity: LearningSensitivityPublic})
	require.ErrorContains(t, err, "control")
}

func TestLearningObservationDuplicateHintsArePublicBoundedAndUnique(t *testing.T) {
	public := LearningCaptureDecision{PublicProjection: true, SafeFingerprint: "sha256:test"}
	ids := []string{"learn-1", "learn-1", "", "learn-2", "learn-3", "learn-4", "learn-5", "learn-6", "learn-7", "learn-8", "learn-9", "learn-10", "learn-11"}
	require.Equal(t, []string{"learn-1", "learn-2", "learn-3", "learn-4", "learn-5", "learn-6", "learn-7", "learn-8", "learn-9", "learn-10"}, LearningObservationDuplicateHints(public, ids))
	public.PublicProjection = false
	require.Nil(t, LearningObservationDuplicateHints(public, ids))
}
