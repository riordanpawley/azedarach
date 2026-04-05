package naming

import (
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
type IssueID struct {
	value string
}

// SessionID is a validated tmux session identifier.
type SessionID struct {
	value string
}

func ParseIssueID(raw string) (IssueID, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return IssueID{}, fmt.Errorf("%w: empty", ErrInvalidIssueID)
	}
	for _, r := range trimmed {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			continue
		}
		return IssueID{}, fmt.Errorf("%w: disallowed character %q", ErrInvalidIssueID, r)
	}
	return IssueID{value: trimmed}, nil
}

func (id IssueID) String() string {
	return id.value
}

func (id IssueID) IsZero() bool {
	return id.value == ""
}

func ParseSessionID(raw, projectPath string) (SessionID, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return SessionID{}, fmt.Errorf("%w: empty", ErrInvalidSessionID)
	}
	if _, ok := ParseIssueIDFromSessionName(trimmed, projectPath); !ok {
		return SessionID{}, fmt.Errorf("%w: %q does not match project naming scope", ErrInvalidSessionID, trimmed)
	}
	return SessionID{value: trimmed}, nil
}

func CanonicalSessionIDForIssue(projectPath string, issueID IssueID) SessionID {
	return SessionID{value: CanonicalSessionID(projectPath, issueID.String())}
}

func (id SessionID) String() string {
	return id.value
}

func (id SessionID) IsZero() bool {
	return id.value == ""
}

