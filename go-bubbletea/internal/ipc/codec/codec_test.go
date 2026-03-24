package codec

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestRoundTripEnvelopeVariants(t *testing.T) {
	c := NewCodec()
	now := time.Date(2026, time.March, 24, 12, 0, 0, 0, time.UTC)

	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-1",
		Kind:            protocol.EnvelopeKindCommand,
		Meta: protocol.Metadata{
			ProjectID:           "proj-1",
			ClientID:            "client-1",
			CorrelationID:       "corr-1",
			LastAppliedRevision: 11,
		},
		Command: "session.start",
		SentAt:  now,
		Body:    []byte(`{"issue":"aej"}`),
	}

	encodedReq := mustEncode(t, c, req)
	framedReq := mustFrame(t, c, encodedReq)
	decodedPayload := mustDecodeFrame(t, c, framedReq)

	var decodedReq protocol.RequestEnvelope
	if err := c.Decode(decodedPayload, &decodedReq); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if decodedReq.RequestID != req.RequestID || decodedReq.Command != req.Command {
		t.Fatalf("decoded request mismatch: %+v", decodedReq)
	}

	resp := protocol.ResponseEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		Meta:            req.Meta,
		Revision:        12,
		CompletedAt:     now.Add(50 * time.Millisecond),
		OK:              true,
		Body:            []byte(`{"ok":true}`),
	}
	var decodedResp protocol.ResponseEnvelope
	if err := c.Decode(mustEncode(t, c, resp), &decodedResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !decodedResp.OK || decodedResp.Revision != 12 {
		t.Fatalf("decoded response mismatch: %+v", decodedResp)
	}

	event := protocol.EventEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		ProjectID:       "proj-1",
		Meta:            req.Meta,
		Revision:        13,
		Event:           "session.started",
		Kind:            protocol.EnvelopeKindEvent,
		EmittedAt:       now,
		Body:            []byte(`{"session":"s-1"}`),
	}
	var decodedEvent protocol.EventEnvelope
	if err := c.Decode(mustEncode(t, c, event), &decodedEvent); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if decodedEvent.Event != event.Event || decodedEvent.Revision != 13 {
		t.Fatalf("decoded event mismatch: %+v", decodedEvent)
	}
}

func TestMalformedFrameAndPayload(t *testing.T) {
	c := NewCodec()

	if _, err := c.DecodeFrame([]byte{0x01, 0x02, 0x03}); err == nil {
		t.Fatal("expected short frame error")
	}

	declaresFour := []byte{0x00, 0x00, 0x00, 0x04, 0x01}
	if _, err := c.DecodeFrame(declaresFour); err == nil {
		t.Fatal("expected invalid frame error")
	}

	var req protocol.RequestEnvelope
	if err := c.Decode([]byte{0xc1}, &req); err == nil {
		t.Fatal("expected invalid payload error")
	}
}

func TestGoldenRequestEnvelopeFixture(t *testing.T) {
	c := NewCodec()
	now := time.Date(2026, time.March, 24, 12, 0, 0, 0, time.UTC)
	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "golden-1",
		Kind:            protocol.EnvelopeKindCommand,
		Meta: protocol.Metadata{
			ProjectID:     "proj-g",
			ClientID:      "client-g",
			CorrelationID: "corr-g",
		},
		Command: "session.attach",
		SentAt:  now,
		Body:    []byte(`{"session":"s-1"}`),
	}

	encoded := mustEncode(t, c, req)
	gotHex := hex.EncodeToString(encoded)
	wantHex := readGoldenHex(t, "request_envelope.hex.golden")

	if gotHex != wantHex {
		t.Fatalf("golden mismatch\nwant: %s\ngot:  %s", wantHex, gotHex)
	}
}

func FuzzDecodeFrame(f *testing.F) {
	c := NewCodec()
	f.Add([]byte{0, 0, 0, 1, 0xff})
	f.Add([]byte{0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = c.DecodeFrame(data)
	})
}

func mustEncode(t *testing.T, c *Codec, v any) []byte {
	t.Helper()
	payload, err := c.Encode(v)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return payload
}

func mustFrame(t *testing.T, c *Codec, payload []byte) []byte {
	t.Helper()
	frame, err := c.EncodeFrame(payload)
	if err != nil {
		t.Fatalf("encode frame: %v", err)
	}
	return frame
}

func mustDecodeFrame(t *testing.T, c *Codec, frame []byte) []byte {
	t.Helper()
	payload, err := c.DecodeFrame(frame)
	if err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	return payload
}

func readGoldenHex(t *testing.T, filename string) string {
	t.Helper()
	path := filepath.Join("testdata", filename)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden fixture %q: %v", path, err)
	}
	return string(bytes.TrimSpace(b))
}
