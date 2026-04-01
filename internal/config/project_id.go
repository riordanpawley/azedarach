package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
)

// ProjectIDForRoot returns a deterministic project route ID derived from the
// absolute project root path.
func ProjectIDForRoot(projectRoot string) (string, error) {
	trimmed := strings.TrimSpace(projectRoot)
	if trimmed == "" {
		return "", fmt.Errorf("project root is empty")
	}

	absRoot, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("resolve absolute project root %q: %w", projectRoot, err)
	}
	cleanRoot := filepath.Clean(absRoot)

	sum := sha256.Sum256([]byte(cleanRoot))
	hashPrefix := hex.EncodeToString(sum[:])[:12]

	base := sanitizeProjectIDComponent(filepath.Base(cleanRoot))
	if base == "" {
		base = "project"
	}

	return hashPrefix + "-" + base, nil
}

func sanitizeProjectIDComponent(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		isAlphaNum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isAlphaNum {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
