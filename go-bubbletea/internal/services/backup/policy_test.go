package backup

import (
	"fmt"
	"testing"
	"time"
)

func timeRef(t time.Time) *time.Time {
	return &t
}

func TestShouldRunStaleOnOpen(t *testing.T) {
	now := time.Date(2026, time.March, 6, 7, 8, 9, 0, time.UTC)

	tests := []struct {
		name            string
		lastSuccessful  *time.Time
		intervalMinutes int
		want            bool
	}{
		{
			name:            "runs when no prior successful backup exists",
			lastSuccessful:  nil,
			intervalMinutes: 60,
			want:            true,
		},
		{
			name:            "skips when latest backup is still fresh",
			lastSuccessful:  timeRef(now.Add(-59 * time.Minute)),
			intervalMinutes: 60,
			want:            false,
		},
		{
			name:            "runs when stale interval boundary is reached",
			lastSuccessful:  timeRef(now.Add(-60 * time.Minute)),
			intervalMinutes: 60,
			want:            true,
		},
		{
			name:            "non-positive interval falls back to default policy",
			lastSuccessful:  timeRef(now.Add(-59 * time.Minute)),
			intervalMinutes: 0,
			want:            false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShouldRunStaleOnOpen(now, tt.lastSuccessful, tt.intervalMinutes)
			if got != tt.want {
				t.Fatalf("ShouldRunStaleOnOpen() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldRunWriteCooldown(t *testing.T) {
	now := time.Date(2026, time.March, 6, 7, 8, 9, 0, time.UTC)

	tests := []struct {
		name                  string
		lastSuccessful        *time.Time
		writeCooldownSeconds  int
		want                  bool
	}{
		{
			name:                 "runs when no prior successful backup exists",
			lastSuccessful:       nil,
			writeCooldownSeconds: 300,
			want:                 true,
		},
		{
			name:                 "skips while cooldown window is active",
			lastSuccessful:       timeRef(now.Add(-299 * time.Second)),
			writeCooldownSeconds: 300,
			want:                 false,
		},
		{
			name:                 "runs when cooldown boundary is reached",
			lastSuccessful:       timeRef(now.Add(-300 * time.Second)),
			writeCooldownSeconds: 300,
			want:                 true,
		},
		{
			name:                 "non-positive cooldown falls back to default policy",
			lastSuccessful:       timeRef(now.Add(-299 * time.Second)),
			writeCooldownSeconds: -1,
			want:                 false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShouldRunWriteCooldown(now, tt.lastSuccessful, tt.writeCooldownSeconds)
			if got != tt.want {
				t.Fatalf("ShouldRunWriteCooldown() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPlanRetentionPrune(t *testing.T) {
	tests := []struct {
		name       string
		filenames  []string
		maxBackups int
		want       []string
	}{
		{
			name: "prunes oldest backups beyond max",
			filenames: []string{
				"issues-20260304T120000Z.db",
				"issues-20260303T120000Z.db",
				"issues-20260302T120000Z.db",
				"issues-20260301T120000Z.db",
			},
			maxBackups: 2,
			want: []string{
				"issues-20260302T120000Z.db",
				"issues-20260301T120000Z.db",
			},
		},
		{
			name: "ignores non-backup filenames",
			filenames: []string{
				"issues-20260302T120000Z.db",
				"README.md",
				"issues.db",
				"issues-20260301T120000Z.db",
			},
			maxBackups: 1,
			want: []string{
				"issues-20260301T120000Z.db",
			},
		},
		{
			name: "non-positive max backups falls back to default retention count",
			filenames: func() []string {
				files := make([]string, 0, 32)
				for i := 0; i < 32; i++ {
					files = append(files, fmt.Sprintf("issues-20260101T0000%02dZ.db", i))
				}
				return files
			}(),
			maxBackups: 0,
			want: []string{
				"issues-20260101T000001Z.db",
				"issues-20260101T000000Z.db",
			},
		},
		{
			name: "prune plan is deterministic regardless of input order",
			filenames: []string{
				"issues-20260302T120000Z.db",
				"issues-20260304T120000Z.db",
				"issues-20260301T120000Z.db",
				"issues-20260303T120000Z.db",
			},
			maxBackups: 2,
			want: []string{
				"issues-20260302T120000Z.db",
				"issues-20260301T120000Z.db",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PlanRetentionPrune(tt.filenames, tt.maxBackups)
			if len(got) != len(tt.want) {
				t.Fatalf("PlanRetentionPrune() len = %d, want %d (%v)", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("PlanRetentionPrune()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestFormatBackupFilename(t *testing.T) {
	input := time.Date(2026, time.March, 6, 17, 0, 7, 0, time.FixedZone("AEDT", 11*60*60))
	got := FormatBackupFilename(input)
	want := "issues-20260306T060007Z.db"
	if got != want {
		t.Fatalf("FormatBackupFilename() = %q, want %q", got, want)
	}
}

func TestParseBackupFilenameTimestamp(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     time.Time
		wantOK   bool
	}{
		{
			name:     "parses valid backup filename",
			filename: "issues-20260305T120001Z.db",
			want:     time.Date(2026, time.March, 5, 12, 0, 1, 0, time.UTC),
			wantOK:   true,
		},
		{
			name:     "rejects non-backup filename",
			filename: "issues.db",
			wantOK:   false,
		},
		{
			name:     "rejects invalid date token",
			filename: "issues-20260230T120001Z.db",
			wantOK:   false,
		},
		{
			name:     "rejects missing zulu suffix",
			filename: "issues-20260305T120001.db",
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseBackupFilenameTimestamp(tt.filename)
			if ok != tt.wantOK {
				t.Fatalf("ParseBackupFilenameTimestamp() ok = %v, want %v", ok, tt.wantOK)
			}
			if tt.wantOK && !got.Equal(tt.want) {
				t.Fatalf("ParseBackupFilenameTimestamp() time = %s, want %s", got, tt.want)
			}
		})
	}
}
