package startup

import (
	"errors"
	"testing"
)

type fakeChecker struct {
	missing map[string]bool
}

func (f fakeChecker) LookPath(name string) (string, error) {
	if f.missing[name] {
		return "", errors.New("missing from PATH")
	}
	return "/usr/bin/" + name, nil
}

func TestCheckStartupHealthWithChecker_Matrix(t *testing.T) {
	tests := []struct {
		name             string
		missing          map[string]bool
		wantHealthy      bool
		wantDegraded     bool
		wantMissingCount int
	}{
		{
			name:             "all required tools available",
			missing:          map[string]bool{},
			wantHealthy:      true,
			wantDegraded:     false,
			wantMissingCount: 0,
		},
		{
			name: "single required tool missing",
			missing: map[string]bool{
				"tmux": true,
			},
			wantHealthy:      false,
			wantDegraded:     true,
			wantMissingCount: 1,
		},
		{
			name: "multiple required tools missing",
			missing: map[string]bool{
				"bd":  true,
				"gh":  true,
				"git": true,
			},
			wantHealthy:      false,
			wantDegraded:     true,
			wantMissingCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := CheckStartupHealthWithChecker(fakeChecker{missing: tt.missing})

			if report.Healthy != tt.wantHealthy {
				t.Fatalf("healthy = %v, want %v", report.Healthy, tt.wantHealthy)
			}

			if report.Degraded != tt.wantDegraded {
				t.Fatalf("degraded = %v, want %v", report.Degraded, tt.wantDegraded)
			}

			if len(report.MissingRequired) != tt.wantMissingCount {
				t.Fatalf("missing count = %d, want %d", len(report.MissingRequired), tt.wantMissingCount)
			}

			if len(report.Checks) != 4 {
				t.Fatalf("checks count = %d, want 4", len(report.Checks))
			}
		})
	}
}

func TestBuildDegradedModeReport(t *testing.T) {
	report := BuildDegradedModeReport(StartupHealthReport{
		Degraded:        true,
		MissingRequired: []string{"tmux", "bd"},
	})

	if !report.Enabled {
		t.Fatalf("enabled = false, want true")
	}

	if len(report.MissingTools) != 2 {
		t.Fatalf("missing tools = %d, want 2", len(report.MissingTools))
	}

	if report.MissingTools[0] != "bd" || report.MissingTools[1] != "tmux" {
		t.Fatalf("missing tools order = %v, want [bd tmux]", report.MissingTools)
	}

	if len(report.DisabledAreas) != 2 {
		t.Fatalf("disabled areas = %d, want 2", len(report.DisabledAreas))
	}

	if report.Message == "" {
		t.Fatalf("message should not be empty")
	}
}
