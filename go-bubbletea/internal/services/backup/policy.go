package backup

import (
	"fmt"
	"regexp"
	"sort"
	"time"
)

const (
	// DefaultIntervalMinutes is the stale-on-open threshold.
	DefaultIntervalMinutes = 60
	// DefaultWriteCooldownSeconds gates post-mutation backup attempts.
	DefaultWriteCooldownSeconds = 300
	// DefaultMaxBackups is the fallback retention count.
	DefaultMaxBackups = 30
)

const backupTimestampLayout = "20060102T150405Z"

var backupFilenamePattern = regexp.MustCompile(`^issues-(\d{8}T\d{6}Z)\.db$`)

type namedBackupFile struct {
	name      string
	timestamp time.Time
}

func normalizePositiveInteger(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

// ShouldRunStaleOnOpen returns true when stale-on-open policy requires a backup attempt.
func ShouldRunStaleOnOpen(now time.Time, lastSuccessfulBackup *time.Time, intervalMinutes int) bool {
	if lastSuccessfulBackup == nil {
		return true
	}

	interval := time.Duration(normalizePositiveInteger(intervalMinutes, DefaultIntervalMinutes)) * time.Minute
	return now.Sub(lastSuccessfulBackup.UTC()) >= interval
}

// ShouldRunWriteCooldown returns true when cooldown policy allows a post-write backup attempt.
func ShouldRunWriteCooldown(now time.Time, lastSuccessfulBackup *time.Time, writeCooldownSeconds int) bool {
	if lastSuccessfulBackup == nil {
		return true
	}

	cooldown := time.Duration(normalizePositiveInteger(writeCooldownSeconds, DefaultWriteCooldownSeconds)) * time.Second
	return now.Sub(lastSuccessfulBackup.UTC()) >= cooldown
}

// FormatBackupFilename renders a backup filename in the required UTC format.
func FormatBackupFilename(at time.Time) string {
	return fmt.Sprintf("issues-%s.db", at.UTC().Format(backupTimestampLayout))
}

// ParseBackupFilenameTimestamp parses a backup timestamp from the canonical filename format.
func ParseBackupFilenameTimestamp(filename string) (time.Time, bool) {
	match := backupFilenamePattern.FindStringSubmatch(filename)
	if len(match) != 2 {
		return time.Time{}, false
	}

	parsed, err := time.Parse(backupTimestampLayout, match[1])
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

// PlanRetentionPrune returns backup filenames that should be pruned to satisfy maxBackups retention.
func PlanRetentionPrune(filenames []string, maxBackups int) []string {
	keepCount := normalizePositiveInteger(maxBackups, DefaultMaxBackups)
	parsed := make([]namedBackupFile, 0, len(filenames))
	for _, filename := range filenames {
		timestamp, ok := ParseBackupFilenameTimestamp(filename)
		if !ok {
			continue
		}
		parsed = append(parsed, namedBackupFile{
			name:      filename,
			timestamp: timestamp,
		})
	}

	sort.Slice(parsed, func(i, j int) bool {
		if !parsed[i].timestamp.Equal(parsed[j].timestamp) {
			return parsed[i].timestamp.After(parsed[j].timestamp)
		}
		return parsed[i].name > parsed[j].name
	})

	if len(parsed) <= keepCount {
		return []string{}
	}

	toPrune := parsed[keepCount:]
	plan := make([]string, 0, len(toPrune))
	for _, entry := range toPrune {
		plan = append(plan, entry.name)
	}
	return plan
}
