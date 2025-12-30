package overlay

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/diagnostics"
)

// Mock diagnostics service for testing
type mockDiagnosticsService struct {
	diagnostics *diagnostics.SystemDiagnostics
}

func (m *mockDiagnosticsService) CollectDiagnostics(ctx context.Context, sessions map[string]*domain.Session, beadsPath *string) *diagnostics.SystemDiagnostics {
	return m.diagnostics
}

func TestNewDiagnosticsPanel(t *testing.T) {
	mockService := &mockDiagnosticsService{
		diagnostics: &diagnostics.SystemDiagnostics{
			OverallState: diagnostics.HealthHealthy,
		},
	}
	sessions := make(map[string]*domain.Session)

	panel := NewDiagnosticsPanel(mockService, sessions)

	if panel == nil {
		t.Fatal("NewDiagnosticsPanel returned nil")
	}
	if panel.diagnosticsService == nil {
		t.Error("diagnosticsService not set")
	}
	if panel.sessions == nil {
		t.Error("sessions not set")
	}
	if panel.activeSection != SectionOverview {
		t.Errorf("activeSection = %v, want %v", panel.activeSection, SectionOverview)
	}
}

func TestDiagnosticsPanel_Init(t *testing.T) {
	mockService := &mockDiagnosticsService{
		diagnostics: &diagnostics.SystemDiagnostics{
			OverallState: diagnostics.HealthHealthy,
		},
	}
	sessions := make(map[string]*domain.Session)

	panel := NewDiagnosticsPanel(mockService, sessions)
	cmd := panel.Init()

	if cmd == nil {
		t.Error("Init() returned nil command")
	}
}

func TestDiagnosticsPanel_Update_KeyHandling(t *testing.T) {
	now := time.Now()
	mockService := &mockDiagnosticsService{
		diagnostics: &diagnostics.SystemDiagnostics{
			Timestamp:    now,
			OverallState: diagnostics.HealthHealthy,
			Sessions:     []diagnostics.SessionInfo{},
			Ports:        []diagnostics.PortInfo{},
			Worktrees:    []diagnostics.WorktreeInfo{},
			Network: diagnostics.NetworkInfo{
				IsOnline:  true,
				LastCheck: now,
			},
			System: diagnostics.SystemInfo{
				GoVersion:    "go1.21",
				OS:           "linux",
				Arch:         "amd64",
				NumGoroutine: 10,
				MemoryUsage:  1024 * 1024,
			},
		},
	}
	sessions := make(map[string]*domain.Session)

	tests := []struct {
		name           string
		key            string
		initialSection DiagnosticsSection
		wantSection    DiagnosticsSection
		wantClose      bool
	}{
		{
			name:           "q closes overlay",
			key:            "q",
			initialSection: SectionOverview,
			wantSection:    SectionOverview,
			wantClose:      true,
		},
		{
			name:           "esc closes overlay",
			key:            "esc",
			initialSection: SectionOverview,
			wantSection:    SectionOverview,
			wantClose:      true,
		},
		{
			name:           "tab switches section",
			key:            "tab",
			initialSection: SectionOverview,
			wantSection:    SectionPorts,
			wantClose:      false,
		},
		{
			name:           "1 switches to overview",
			key:            "1",
			initialSection: SectionPorts,
			wantSection:    SectionOverview,
			wantClose:      false,
		},
		{
			name:           "2 switches to ports",
			key:            "2",
			initialSection: SectionOverview,
			wantSection:    SectionPorts,
			wantClose:      false,
		},
		{
			name:           "3 switches to sessions",
			key:            "3",
			initialSection: SectionOverview,
			wantSection:    SectionSessions,
			wantClose:      false,
		},
		{
			name:           "4 switches to worktrees",
			key:            "4",
			initialSection: SectionOverview,
			wantSection:    SectionWorktrees,
			wantClose:      false,
		},
		{
			name:           "5 switches to network",
			key:            "5",
			initialSection: SectionOverview,
			wantSection:    SectionNetwork,
			wantClose:      false,
		},
		{
			name:           "6 switches to system",
			key:            "6",
			initialSection: SectionOverview,
			wantSection:    SectionSystem,
			wantClose:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panel := NewDiagnosticsPanel(mockService, sessions)
			panel.activeSection = tt.initialSection
			panel.currentDiagnostics = mockService.diagnostics

			msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tt.key)}
			if tt.key == "esc" {
				msg = tea.KeyMsg{Type: tea.KeyEsc}
			}

			newModel, cmd := panel.Update(msg)
			newPanel := newModel.(*DiagnosticsPanel)

			if newPanel.activeSection != tt.wantSection {
				t.Errorf("activeSection = %v, want %v", newPanel.activeSection, tt.wantSection)
			}

			// Check if CloseOverlayMsg was returned
			if tt.wantClose {
				if cmd == nil {
					t.Error("expected CloseOverlayMsg command, got nil")
				}
			}
		})
	}
}

func TestDiagnosticsPanel_Update_Scrolling(t *testing.T) {
	now := time.Now()
	mockService := &mockDiagnosticsService{
		diagnostics: &diagnostics.SystemDiagnostics{
			Timestamp:    now,
			OverallState: diagnostics.HealthHealthy,
			Sessions:     []diagnostics.SessionInfo{},
			Ports:        []diagnostics.PortInfo{},
			Worktrees:    []diagnostics.WorktreeInfo{},
			Network: diagnostics.NetworkInfo{
				IsOnline:  true,
				LastCheck: now,
			},
			System: diagnostics.SystemInfo{
				GoVersion:    "go1.21",
				OS:           "linux",
				Arch:         "amd64",
				NumGoroutine: 10,
				MemoryUsage:  1024 * 1024,
			},
		},
	}
	sessions := make(map[string]*domain.Session)

	panel := NewDiagnosticsPanel(mockService, sessions)
	panel.currentDiagnostics = mockService.diagnostics
	panel.contentHeight = 50
	panel.scrollY = 0

	// Test scroll down
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}
	newModel, _ := panel.Update(msg)
	newPanel := newModel.(*DiagnosticsPanel)

	if newPanel.scrollY != 1 {
		t.Errorf("after 'j', scrollY = %v, want 1", newPanel.scrollY)
	}

	// Test scroll up
	msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")}
	newModel, _ = newPanel.Update(msg)
	newPanel = newModel.(*DiagnosticsPanel)

	if newPanel.scrollY != 0 {
		t.Errorf("after 'k', scrollY = %v, want 0", newPanel.scrollY)
	}

	// Test jump to top
	newPanel.scrollY = 10
	msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")}
	newModel, _ = newPanel.Update(msg)
	newPanel = newModel.(*DiagnosticsPanel)

	if newPanel.scrollY != 0 {
		t.Errorf("after 'g', scrollY = %v, want 0", newPanel.scrollY)
	}

	// Test jump to bottom
	msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")}
	newModel, _ = newPanel.Update(msg)
	newPanel = newModel.(*DiagnosticsPanel)

	maxScroll := newPanel.maxScroll()
	if newPanel.scrollY != maxScroll {
		t.Errorf("after 'G', scrollY = %v, want %v", newPanel.scrollY, maxScroll)
	}
}

func TestDiagnosticsPanel_Title(t *testing.T) {
	tests := []struct {
		name        string
		section     DiagnosticsSection
		status      diagnostics.HealthStatus
		wantSection string
		wantStatus  string
	}{
		{
			name:        "overview healthy",
			section:     SectionOverview,
			status:      diagnostics.HealthHealthy,
			wantSection: "Overview",
			wantStatus:  "HEALTHY",
		},
		{
			name:        "ports degraded",
			section:     SectionPorts,
			status:      diagnostics.HealthDegraded,
			wantSection: "Ports",
			wantStatus:  "DEGRADED",
		},
		{
			name:        "network critical",
			section:     SectionNetwork,
			status:      diagnostics.HealthCritical,
			wantSection: "Network",
			wantStatus:  "CRITICAL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockService := &mockDiagnosticsService{
				diagnostics: &diagnostics.SystemDiagnostics{
					OverallState: tt.status,
				},
			}
			sessions := make(map[string]*domain.Session)

			panel := NewDiagnosticsPanel(mockService, sessions)
			panel.activeSection = tt.section
			panel.currentDiagnostics = mockService.diagnostics

			title := panel.Title()

			if !strings.Contains(title, tt.wantSection) {
				t.Errorf("Title() = %v, want to contain %v", title, tt.wantSection)
			}
			if !strings.Contains(title, tt.wantStatus) {
				t.Errorf("Title() = %v, want to contain %v", title, tt.wantStatus)
			}
		})
	}
}

func TestDiagnosticsPanel_View(t *testing.T) {
	now := time.Now()
	mockService := &mockDiagnosticsService{
		diagnostics: &diagnostics.SystemDiagnostics{
			Timestamp:    now,
			OverallState: diagnostics.HealthHealthy,
			Sessions:     []diagnostics.SessionInfo{},
			Ports:        []diagnostics.PortInfo{},
			Worktrees:    []diagnostics.WorktreeInfo{},
			Network: diagnostics.NetworkInfo{
				IsOnline:  true,
				LastCheck: now,
			},
			System: diagnostics.SystemInfo{
				GoVersion:    "go1.21",
				OS:           "linux",
				Arch:         "amd64",
				NumGoroutine: 10,
				MemoryUsage:  1024 * 1024,
			},
		},
	}
	sessions := make(map[string]*domain.Session)

	panel := NewDiagnosticsPanel(mockService, sessions)
	panel.currentDiagnostics = mockService.diagnostics

	// Test each section renders without panic
	sections := []DiagnosticsSection{
		SectionOverview,
		SectionPorts,
		SectionSessions,
		SectionWorktrees,
		SectionNetwork,
		SectionSystem,
	}

	for _, section := range sections {
		t.Run(panel.getSectionName(), func(t *testing.T) {
			panel.activeSection = section
			view := panel.View()

			if view == "" {
				t.Error("View() returned empty string")
			}
		})
	}
}

func TestDiagnosticsPanel_Size(t *testing.T) {
	mockService := &mockDiagnosticsService{}
	sessions := make(map[string]*domain.Session)

	panel := NewDiagnosticsPanel(mockService, sessions)
	width, height := panel.Size()

	if width == 0 {
		t.Error("Size() width = 0")
	}
	if height == 0 {
		t.Error("Size() height = 0")
	}
}

func TestDiagnosticsPanel_GetSectionName(t *testing.T) {
	tests := []struct {
		section DiagnosticsSection
		want    string
	}{
		{SectionOverview, "Overview"},
		{SectionPorts, "Ports"},
		{SectionSessions, "Sessions"},
		{SectionWorktrees, "Worktrees"},
		{SectionNetwork, "Network"},
		{SectionSystem, "System"},
	}

	mockService := &mockDiagnosticsService{}
	sessions := make(map[string]*domain.Session)
	panel := NewDiagnosticsPanel(mockService, sessions)

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			panel.activeSection = tt.section
			got := panel.getSectionName()
			if got != tt.want {
				t.Errorf("getSectionName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTruncateDiagString(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "no truncation needed",
			input:  "hello",
			maxLen: 10,
			want:   "hello",
		},
		{
			name:   "truncation with ellipsis",
			input:  "hello world",
			maxLen: 8,
			want:   "hello...",
		},
		{
			name:   "very short max length",
			input:  "hello",
			maxLen: 2,
			want:   "he",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateDiagString(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateDiagString(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestDiagnosticsPanel_RefreshMessage(t *testing.T) {
	now := time.Now()
	mockService := &mockDiagnosticsService{
		diagnostics: &diagnostics.SystemDiagnostics{
			Timestamp:    now,
			OverallState: diagnostics.HealthHealthy,
		},
	}
	sessions := make(map[string]*domain.Session)

	panel := NewDiagnosticsPanel(mockService, sessions)

	// Simulate receiving a refresh message
	msg := DiagnosticsRefreshMsg{
		Diagnostics: mockService.diagnostics,
	}

	newModel, _ := panel.Update(msg)
	newPanel := newModel.(*DiagnosticsPanel)

	if newPanel.currentDiagnostics == nil {
		t.Error("currentDiagnostics not set after refresh message")
	}
	if newPanel.currentDiagnostics.OverallState != diagnostics.HealthHealthy {
		t.Errorf("OverallState = %v, want %v", newPanel.currentDiagnostics.OverallState, diagnostics.HealthHealthy)
	}
}

func TestFormatBytes_Overlay(t *testing.T) {
	tests := []struct {
		bytes uint64
		want  string
	}{
		{512, "512 B"},
		{1024, "1.00 KB"},
		{1024 * 1024, "1.00 MB"},
		{1024 * 1024 * 1024, "1.00 GB"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatBytes(tt.bytes)
			if got != tt.want {
				t.Errorf("formatBytes(%v) = %v, want %v", tt.bytes, got, tt.want)
			}
		})
	}
}

func TestFormatDuration_Overlay(t *testing.T) {
	tests := []struct {
		duration time.Duration
		want     string
	}{
		{30 * time.Second, "30s"},
		{2 * time.Minute, "2m"},
		{90 * time.Minute, "1h30m"},
		{2 * time.Hour, "2h"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatDuration(tt.duration)
			if got != tt.want {
				t.Errorf("formatDuration(%v) = %v, want %v", tt.duration, got, tt.want)
			}
		})
	}
}
