package domain

import "testing"

func TestLearningContextFingerprintCanonicalAndEvidenceFree(t *testing.T) {
	a, err := LearningContextFingerprint(" ddh ", "req-1", []string{"Go", "go"}, []string{"b.go", "a.go"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := LearningContextFingerprint("ddh", "req-1", []string{"go"}, []string{"a.go", "b.go"})
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("canonical fingerprints differ: %q != %q", a, b)
	}
	if len(a) != 71 || a[:7] != "sha256:" {
		t.Fatalf("fingerprint = %q", a)
	}
}
