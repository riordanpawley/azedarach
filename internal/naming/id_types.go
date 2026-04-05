package naming

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

var (
	// ErrInvalidIssueID indicates the supplied issue identifier is malformed.
	ErrInvalidIssueID = errors.New("invalid issue id")
	// ErrInvalidSessionID indicates the supplied session identifier is malformed.
	ErrInvalidSessionID = errors.New("invalid session id")
	// ErrInvalidOperationID indicates the supplied operation identifier is malformed.
	ErrInvalidOperationID = errors.New("invalid operation id")
	// ErrInvalidRequestID indicates the supplied request identifier is malformed.
	ErrInvalidRequestID = errors.New("invalid request id")
)

// IssueID is a validated issue identifier.
type IssueID string

// SessionID is a validated tmux session identifier.
type SessionID string

// OperationID is a validated operation identifier.
type OperationID string

// RequestID is a validated request correlation identifier.
type RequestID string

func ParseIssueID(raw string) (IssueID, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidIssueID)
	}
	for _, r := range trimmed {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			continue
		}
		return "", fmt.Errorf("%w: disallowed character %q", ErrInvalidIssueID, r)
	}
	return IssueID(trimmed), nil
}

func (id IssueID) String() string {
	return string(id)
}

func (id IssueID) IsZero() bool {
	return strings.TrimSpace(string(id)) == ""
}

func (id IssueID) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.String())
}

func (id *IssueID) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parsed, err := ParseIssueID(raw)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

func (id IssueID) Value() (driver.Value, error) {
	return id.String(), nil
}

func (id *IssueID) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*id = ""
		return nil
	case string:
		parsed, err := ParseIssueID(v)
		if err != nil {
			return err
		}
		*id = parsed
		return nil
	case []byte:
		parsed, err := ParseIssueID(string(v))
		if err != nil {
			return err
		}
		*id = parsed
		return nil
	default:
		return fmt.Errorf("%w: unsupported scan type %T", ErrInvalidIssueID, src)
	}
}

func ParseSessionID(raw, projectPath string) (SessionID, error) {
	trimmed := strings.TrimSpace(raw)
	parsedLoose, err := ParseSessionIDLoose(trimmed)
	if err != nil {
		return "", err
	}
	trimmed = parsedLoose.String()
	if _, ok := ParseIssueIDFromSessionName(trimmed, projectPath); !ok {
		return "", fmt.Errorf("%w: %q does not match project naming scope", ErrInvalidSessionID, trimmed)
	}
	return SessionID(trimmed), nil
}

func ParseSessionIDLoose(raw string) (SessionID, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidSessionID)
	}
	if err := validateIDCharset(trimmed, ErrInvalidSessionID); err != nil {
		return "", err
	}
	return SessionID(trimmed), nil
}

func CanonicalSessionIDForIssue(projectPath string, issueID IssueID) SessionID {
	return SessionID(CanonicalSessionID(projectPath, issueID.String()))
}

func (id SessionID) String() string {
	return string(id)
}

func (id SessionID) IsZero() bool {
	return strings.TrimSpace(string(id)) == ""
}

func (id SessionID) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.String())
}

func (id *SessionID) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	// Project scope is unknown while decoding generic payloads; validate session-safe shape.
	parsed, err := ParseSessionIDLoose(raw)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

func (id SessionID) Value() (driver.Value, error) {
	return id.String(), nil
}

func (id *SessionID) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*id = ""
		return nil
	case string:
		parsed, err := ParseSessionIDLoose(v)
		if err != nil {
			return err
		}
		*id = parsed
		return nil
	case []byte:
		parsed, err := ParseSessionIDLoose(string(v))
		if err != nil {
			return err
		}
		*id = parsed
		return nil
	default:
		return fmt.Errorf("%w: unsupported scan type %T", ErrInvalidSessionID, src)
	}
}

func ParseOperationID(raw string) (OperationID, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidOperationID)
	}
	if err := validateIDCharset(trimmed, ErrInvalidOperationID); err != nil {
		return "", err
	}
	return OperationID(trimmed), nil
}

func (id OperationID) String() string {
	return string(id)
}

func (id OperationID) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.String())
}

func (id *OperationID) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parsed, err := ParseOperationID(raw)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

func ParseRequestID(raw string) (RequestID, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidRequestID)
	}
	if err := validateIDCharset(trimmed, ErrInvalidRequestID); err != nil {
		return "", err
	}
	return RequestID(trimmed), nil
}

func (id RequestID) String() string {
	return string(id)
}

func (id RequestID) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.String())
}

func (id *RequestID) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parsed, err := ParseRequestID(raw)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

func validateIDCharset(trimmed string, sentinel error) error {
	for _, r := range trimmed {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			continue
		}
		return fmt.Errorf("%w: disallowed character %q", sentinel, r)
	}
	return nil
}
