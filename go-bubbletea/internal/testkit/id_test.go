package testkit

import "testing"

func TestDeterministicIDGeneratorNext(t *testing.T) {
	generator := NewDeterministicIDGenerator("op-", 7)

	AssertEqual(t, generator.Next(), "op-7", "first id should use start value")
	AssertEqual(t, generator.Next(), "op-8", "second id should increment")
	AssertEqual(t, generator.Next(), "op-9", "third id should increment")
}
