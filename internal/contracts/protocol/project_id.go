package protocol

import "strings"

const DefaultProjectID = "default"

// TrimProjectID returns the canonical trimmed project identifier without applying a fallback.
func TrimProjectID(projectID string) string {
	return strings.TrimSpace(projectID)
}

// NormalizeProjectID trims a project identifier and falls back to the default route when empty.
func NormalizeProjectID(projectID string) string {
	if trimmed := TrimProjectID(projectID); trimmed != "" {
		return trimmed
	}
	return DefaultProjectID
}
