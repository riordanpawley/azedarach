package testkit

import (
	"errors"
	"strings"
	"testing"
)

func AssertEqual[T comparable](t *testing.T, got T, want T, context string) {
	t.Helper()

	if got != want {
		t.Fatalf("%s: got %v, want %v", context, got, want)
	}
}

func AssertTrue(t *testing.T, condition bool, context string) {
	t.Helper()

	if !condition {
		t.Fatalf("%s: expected true", context)
	}
}

func AssertNoError(t *testing.T, err error, context string) {
	t.Helper()

	if err != nil {
		t.Fatalf("%s: unexpected error: %v", context, err)
	}
}

func AssertErrorIs(t *testing.T, err error, target error, context string) {
	t.Helper()

	if !errors.Is(err, target) {
		t.Fatalf("%s: got error %v, want errors.Is(..., %v)", context, err, target)
	}
}

func AssertErrorContains(t *testing.T, err error, substring string, context string) {
	t.Helper()

	if err == nil {
		t.Fatalf("%s: expected error containing %q, got nil", context, substring)
	}

	if !strings.Contains(err.Error(), substring) {
		t.Fatalf("%s: error %q does not contain %q", context, err.Error(), substring)
	}
}
