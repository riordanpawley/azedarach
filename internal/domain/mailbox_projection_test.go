package domain

import (
	"reflect"
	"testing"
)

func TestCanonicalMailboxProducerPayload(t *testing.T) {
	payload := map[string]any{
		"publication":                "review_ready_observation_replay.v1",
		"worker_evidence":            map[string]any{"schema": "worker_evidence.v1"},
		"worker_evidence_validation": map[string]any{"complete": true},
	}
	want := map[string]any{"publication": "review_ready_observation_replay.v1"}
	if got := CanonicalMailboxProducerPayload(payload); !reflect.DeepEqual(got, want) {
		t.Fatalf("canonical payload = %#v, want %#v", got, want)
	}
	if _, ok := payload["worker_evidence"]; !ok {
		t.Fatal("canonicalization mutated its input")
	}
}
