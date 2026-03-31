package naming

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

const (
	defaultBranchSlugMaxLength = 24
	sessionEscapePrefix        = "_x"
	sessionEscapeSuffix        = "_"
)

var (
	projectSessionPrefixPattern = regexp.MustCompile(`^[a-z]{2}-`)
	sessionEscapePattern        = regexp.MustCompile(`_x([0-9a-f]+)_`)
)

func ProjectSessionPrefix(projectPath string) string {
	projectName := strings.ToLower(filepath.Base(strings.TrimSpace(projectPath)))
	var b strings.Builder
	for _, r := range projectName {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	lettersOnly := b.String()
	switch len(lettersOnly) {
	case 0:
		return "az"
	case 1:
		return lettersOnly + "x"
	default:
		return lettersOnly[:2]
	}
}

func encodeSessionIssueID(issueID string) string {
	var b strings.Builder
	for _, r := range issueID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
			continue
		}
		b.WriteString(sessionEscapePrefix)
		b.WriteString(strconv.FormatInt(int64(r), 16))
		b.WriteString(sessionEscapeSuffix)
	}
	return b.String()
}

func decodeSessionIssueID(value string) string {
	return sessionEscapePattern.ReplaceAllStringFunc(value, func(fragment string) string {
		matches := sessionEscapePattern.FindStringSubmatch(fragment)
		if len(matches) != 2 {
			return fragment
		}
		codepoint, err := strconv.ParseInt(matches[1], 16, 32)
		if err != nil {
			return fragment
		}
		return string(rune(codepoint))
	})
}

func CanonicalSessionID(projectPath, issueID string) string {
	trimmedIssueID := strings.TrimSpace(issueID)
	return ProjectSessionPrefix(projectPath) + "-" + encodeSessionIssueID(trimmedIssueID)
}

func ParseIssueIDFromSessionName(sessionName, projectPath string) (string, bool) {
	trimmedName := strings.TrimSpace(sessionName)
	if trimmedName == "" {
		return "", false
	}

	expectedPrefix := ProjectSessionPrefix(projectPath) + "-"
	if strings.HasPrefix(trimmedName, expectedPrefix) {
		decoded := strings.TrimSpace(decodeSessionIssueID(strings.TrimPrefix(trimmedName, expectedPrefix)))
		if decoded == "" {
			return "", false
		}
		return decoded, true
	}

	if projectSessionPrefixPattern.MatchString(trimmedName) {
		return "", false
	}

	decoded := strings.TrimSpace(decodeSessionIssueID(trimmedName))
	if decoded == "" {
		return "", false
	}
	return decoded, true
}

func SanitizeIssueIDForBranchSegment(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastWasDash := false
	for _, r := range normalized {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			if r == '-' {
				if lastWasDash {
					continue
				}
				lastWasDash = true
			} else {
				lastWasDash = false
			}
			b.WriteRune(r)
			continue
		}
		if !lastWasDash {
			b.WriteRune('-')
			lastWasDash = true
		}
	}
	sanitized := strings.Trim(b.String(), "-")
	if sanitized == "" {
		return "issue"
	}
	return sanitized
}

func SlugifyIssueTitleForBranch(title string, maxLength int) string {
	if maxLength <= 0 {
		maxLength = defaultBranchSlugMaxLength
	}
	var b strings.Builder
	lastWasDash := false
	for _, r := range strings.ToLower(title) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastWasDash = false
			continue
		}
		if !lastWasDash {
			b.WriteRune('-')
			lastWasDash = true
		}
	}
	base := strings.Trim(b.String(), "-")
	if base == "" {
		base = "task"
	}
	if len(base) <= maxLength {
		return base
	}
	trimmed := strings.TrimRight(base[:maxLength], "-")
	if trimmed == "" {
		return "task"
	}
	return trimmed
}

func ComposeIssueBranchName(author, issueID, issueTitle string, maxLength int) string {
	sanitizedAuthor := SanitizeBranchAuthor(author)
	if sanitizedAuthor == "" {
		sanitizedAuthor = "author"
	}
	issueSegment := SanitizeIssueIDForBranchSegment(issueID)
	slug := SlugifyIssueTitleForBranch(issueTitle, maxLength)
	return sanitizedAuthor + "/" + issueSegment + "/" + slug
}

func SanitizeBranchAuthor(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func IssueIDsEqual(left, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}

func ExtractIssueIDFromBranchName(branchName string) (string, bool) {
	trimmed := strings.TrimSpace(branchName)
	if trimmed == "" {
		return "", false
	}

	parts := strings.Split(trimmed, "/")
	if len(parts) >= 3 {
		issueID := strings.TrimSpace(parts[1])
		if issueID != "" {
			return issueID, true
		}
	}

	if len(parts) == 2 && strings.EqualFold(parts[0], "az") {
		issueID := strings.TrimSpace(parts[1])
		if issueID != "" {
			return issueID, true
		}
	}

	return "", false
}
