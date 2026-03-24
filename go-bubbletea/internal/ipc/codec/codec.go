package codec

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/vmihailenco/msgpack/v5"
)

// MaxFrameSize bounds a single IPC frame payload.
const MaxFrameSize = 16 << 20 // 16 MiB

// ErrorKind identifies typed codec and framing failures.
type ErrorKind string

const (
	ErrorKindInvalidPayload ErrorKind = "invalid_payload"
	ErrorKindInvalidFrame   ErrorKind = "invalid_frame"
	ErrorKindFrameTooLarge  ErrorKind = "frame_too_large"
	ErrorKindShortFrame     ErrorKind = "short_frame"
)

// Error is a typed codec error wrapper.
type Error struct {
	Kind ErrorKind
	Err  error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return string(e.Kind)
	}
	return fmt.Sprintf("%s: %v", e.Kind, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// Codec handles MessagePack payload and length-prefixed frame encoding.
type Codec struct {
	maxFrameSize uint32
}

// NewCodec returns a codec with default framing constraints.
func NewCodec() *Codec {
	return &Codec{maxFrameSize: MaxFrameSize}
}

// Encode marshals a typed value to MessagePack payload bytes.
func (c *Codec) Encode(v any) ([]byte, error) {
	payload, err := msgpack.Marshal(v)
	if err != nil {
		return nil, &Error{Kind: ErrorKindInvalidPayload, Err: err}
	}
	return payload, nil
}

// Decode unmarshals MessagePack payload bytes into out.
func (c *Codec) Decode(payload []byte, out any) error {
	if err := msgpack.Unmarshal(payload, out); err != nil {
		return &Error{Kind: ErrorKindInvalidPayload, Err: err}
	}
	return nil
}

// EncodeFrame serializes a single payload with a 4-byte big-endian length.
func (c *Codec) EncodeFrame(payload []byte) ([]byte, error) {
	if len(payload) > int(c.maxFrameSize) {
		return nil, &Error{
			Kind: ErrorKindFrameTooLarge,
			Err:  fmt.Errorf("payload size %d exceeds max %d", len(payload), c.maxFrameSize),
		}
	}

	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(payload)))
	copy(frame[4:], payload)
	return frame, nil
}

// DecodeFrame validates and extracts a single payload frame.
func (c *Codec) DecodeFrame(frame []byte) ([]byte, error) {
	if len(frame) < 4 {
		return nil, &Error{Kind: ErrorKindShortFrame, Err: io.ErrUnexpectedEOF}
	}

	n := binary.BigEndian.Uint32(frame[:4])
	if n > c.maxFrameSize {
		return nil, &Error{
			Kind: ErrorKindFrameTooLarge,
			Err:  fmt.Errorf("declared frame size %d exceeds max %d", n, c.maxFrameSize),
		}
	}

	if len(frame[4:]) != int(n) {
		return nil, &Error{
			Kind: ErrorKindInvalidFrame,
			Err:  errors.New("declared frame size does not match payload length"),
		}
	}
	return frame[4:], nil
}

// WriteFrame writes a single framed payload to an io.Writer.
func (c *Codec) WriteFrame(w io.Writer, payload []byte) error {
	frame, err := c.EncodeFrame(payload)
	if err != nil {
		return err
	}
	_, err = w.Write(frame)
	return err
}

// ReadFrame reads a single framed payload from an io.Reader.
func (c *Codec) ReadFrame(r io.Reader) ([]byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, &Error{Kind: ErrorKindShortFrame, Err: err}
	}

	n := binary.BigEndian.Uint32(header)
	if n > c.maxFrameSize {
		return nil, &Error{
			Kind: ErrorKindFrameTooLarge,
			Err:  fmt.Errorf("declared frame size %d exceeds max %d", n, c.maxFrameSize),
		}
	}

	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, &Error{Kind: ErrorKindShortFrame, Err: err}
	}
	return payload, nil
}
