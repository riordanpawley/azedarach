package tmux

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/riordanpawley/azedarach/internal/domain"
)

// PasteAgentTextAndSubmit runs paste and submit in one tmux command queue so
// client input cannot interleave between the text and Enter operations.
func (c *Client) PasteAgentTextAndSubmit(ctx context.Context, paneID, text string) error {
	bufferName := "azedarach-agent-input-" + safeTmuxBufferSuffix(paneID)
	inputRunner, ok := c.runner.(InputCommandRunner)
	if !ok {
		return &domain.TmuxError{Op: "load-buffer", Session: paneID, Err: errors.New("tmux runner does not support stdin payloads")}
	}
	if _, err := inputRunner.RunWithInput(ctx, text, "load-buffer", "-b", bufferName, "-"); err != nil {
		return &domain.TmuxError{Op: "load-buffer", Session: paneID, Err: err}
	}
	if _, err := c.runner.Run(ctx, "paste-buffer", "-dp", "-b", bufferName, "-t", paneID, ";", "send-keys", "-t", paneID, "Enter"); err != nil {
		return &domain.TmuxError{Op: "paste-and-submit", Session: paneID, Err: err}
	}
	return nil
}

// ObserveAgentInputTarget reads only non-sensitive metadata plus a pane
// capture. Callers must never log Capture.
type AgentInputTargetObservation struct {
	Pane           PaneInfo
	CurrentCommand string
	AttachedCount  int
	Capture        string
}

func (c *Client) ObserveAgentInputTarget(ctx context.Context, sessionID, paneID string) (AgentInputTargetObservation, error) {
	format := "#{session_name}\t#{pane_id}\t#{pane_pid}\t#{pane_current_command}\t#{session_attached}"
	out, err := c.runner.Run(ctx, "list-panes", "-a", "-F", format)
	if err != nil {
		return AgentInputTargetObservation{}, &domain.TmuxError{Op: "list-panes", Session: sessionID, Err: err}
	}
	wantPane := sanitizePaneID(paneID)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.SplitN(line, "\t", 5)
		if len(parts) != 5 || strings.TrimSpace(parts[0]) != strings.TrimSpace(sessionID) || sanitizePaneID(parts[1]) != wantPane {
			continue
		}
		pid, pidErr := parsePositiveInt(parts[2])
		attached, attachedErr := parseNonNegativeInt(parts[4])
		if pidErr != nil || attachedErr != nil {
			return AgentInputTargetObservation{}, fmt.Errorf("observe agent input target: invalid tmux metadata")
		}
		capture, captureErr := c.CapturePane(ctx, paneID, 12)
		if captureErr != nil {
			return AgentInputTargetObservation{}, captureErr
		}
		return AgentInputTargetObservation{Pane: PaneInfo{SessionName: sessionID, PaneID: wantPane, PanePID: pid}, CurrentCommand: strings.TrimSpace(parts[3]), AttachedCount: attached, Capture: capture}, nil
	}
	return AgentInputTargetObservation{}, fmt.Errorf("observe agent input target: pane is not live")
}

func parsePositiveInt(value string) (int, error) {
	n, err := parseNonNegativeInt(value)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("not positive")
	}
	return n, nil
}

func parseNonNegativeInt(value string) (int, error) {
	n := 0
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("empty integer")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid integer")
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}
