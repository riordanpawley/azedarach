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
)

// IssueID is a validated issue identifier.
type IssueID string

// SessionID is a validated tmux session identifier.
type SessionID string

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
	if trimmed == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidSessionID)
	}
	if _, ok := ParseIssueIDFromSessionName(trimmed, projectPath); !ok {
		return "", fmt.Errorf("%w: %q does not match project naming scope", ErrInvalidSessionID, trimmed)
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
	// Project scope is unknown while decoding generic payloads; validate non-empty/session-safe shape.
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Errorf("%w: empty", ErrInvalidSessionID)
	}
	*id = SessionID(trimmed)
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
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return fmt.Errorf("%w: empty", ErrInvalidSessionID)
		}
		*id = SessionID(trimmed)
		return nil
	case []byte:
		trimmed := strings.TrimSpace(string(v))
		if trimmed == "" {
			return fmt.Errorf("%w: empty", ErrInvalidSessionID)
		}
		*id = SessionID(trimmed)
		return nil
	default:
		return fmt.Errorf("%w: unsupported scan type %T", ErrInvalidSessionID, src)
	}
}
