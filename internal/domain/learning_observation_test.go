package domain

import "testing"

func TestLearningObservationFingerprintIsStableAndPrivateSafe(t *testing.T) {
	a, err := LearningObservationFingerprint(LearningSensitivityPublic, " Use bounded retries ", map[string]string{" surface ": " cli "})
	if err != nil {
		t.Fatal(err)
	}
	b, err := LearningObservationFingerprint(LearningSensitivityPublic, "use  bounded retries", map[string]string{"surface": "cli"})
	if err != nil || a != b || a == "" {
		t.Fatalf("fingerprints = %q/%q, err=%v", a, b, err)
	}
	private, err := LearningObservationFingerprint(LearningSensitivityPrivate, "secret behavior", map[string]string{"token": "secret"})
	if err != nil || private != "" {
		t.Fatalf("private fingerprint = %q, err=%v", private, err)
	}
}
