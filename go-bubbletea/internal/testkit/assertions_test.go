package testkit

import (
	"errors"
	"testing"
)

func TestAssertionsHelpers(t *testing.T) {
	AssertEqual(t, 1, 1, "equal values should pass")
	AssertTrue(t, true, "true condition should pass")
	AssertNoError(t, nil, "nil error should pass")

	base := errors.New("base")
	wrapped := errors.Join(base, errors.New("extra"))
	AssertErrorIs(t, wrapped, base, "wrapped error should match base")
	AssertErrorContains(t, wrapped, "base", "error should contain text")
}
