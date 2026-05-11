package tmuxselector

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// TestInsertJumpLabelStaysInsideStyledBorder is the regression test for issue
// cfz: with colored borders the "│ " substring never appears literally because
// lipgloss wraps each "│" in ANSI escape codes, so the legacy
// strings.Index-based search returned -1 for every line and the fallback
// prepended the label OUTSIDE the rounded top-left corner, breaking grid
// alignment. After the fix the label must appear inside the card and the
// outer card width must remain unchanged.
func TestInsertJumpLabelStaysInsideStyledBorder(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	started := time.Now().Add(-12*time.Hour - 18*time.Minute)
	row := SessionRow{
		SessionID:             "ch-bgz",
		IssueID:               "bgz",
		TaskTitle:             "fix production crashing bug",
		ProjectID:             "Chefy",
		ProjectPath:           "/Users/riordan/prog/Chefy",
		HasTmuxSession:        true,
		HasWorktree:           true,
		Priority:              domain.P2,
		Type:                  domain.TypeBug,
		IssueStatus:           domain.StatusInProgress,
		State:                 domain.SessionBusy,
		StartedAt:             &started,
		GitAheadCount:         126,
		HasUncommittedChanges: true,
		GitAdditions:          9981,
		GitDeletions:          9443,
	}
	s := styles.New()

	for _, w := range []int{42, 56, 96} {
		card := RenderSessionRow(row, false, w, lipgloss.Style{}, lipgloss.Style{}, lipgloss.Style{}, s)
		labelled := insertJumpLabel(card, "9", s)

		baseLines := strings.Split(card, "\n")
		labelledLines := strings.Split(labelled, "\n")

		if len(baseLines) != len(labelledLines) {
			t.Fatalf("width=%d: line count drifted (base=%d labelled=%d)", w, len(baseLines), len(labelledLines))
		}

		baseStrip := ansi.Strip(baseLines[0])
		labelledStrip := ansi.Strip(labelledLines[0])
		if baseStrip != labelledStrip {
			t.Fatalf("width=%d: top border changed by label insertion\nbefore: %q\nafter:  %q", w, baseStrip, labelledStrip)
		}
		if !strings.HasPrefix(labelledStrip, "╭") {
			t.Fatalf("width=%d: top border no longer starts with ╭ — label leaked outside\n%s", w, labelled)
		}

		for i, line := range labelledLines {
			if got, want := ansi.StringWidth(line), ansi.StringWidth(baseLines[i]); got != want {
				t.Fatalf("width=%d line=%d: visible width changed (was %d, now %d)\nbefore: %q\nafter:  %q",
					w, i, want, got, baseLines[i], line)
			}
		}

		// First content line should now contain the label between the borders.
		var found bool
		for _, line := range labelledLines[1 : len(labelledLines)-1] {
			stripped := ansi.Strip(line)
			if !strings.HasPrefix(stripped, "│") || !strings.HasSuffix(stripped, "│") {
				continue
			}
			if strings.Contains(stripped, " 9 ") || strings.HasPrefix(stripped, "│ 9 ") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("width=%d: label never appeared inside any content line:\n%s", w, labelled)
		}
	}
}
