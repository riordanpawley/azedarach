package styles

import "strings"

// StatusBarContract defines a deterministic status bar rendering contract.
type StatusBarContract struct {
	Mode  string
	Hints string
	Width int
}

// StatusBarSegments normalizes raw mode/hints into deterministic text segments.
func StatusBarSegments(mode, hints string) (modeSegment, separatorSegment, hintsSegment string) {
	normalizedMode := strings.ToUpper(strings.TrimSpace(mode))
	if normalizedMode == "" {
		normalizedMode = "NORMAL"
	}

	return " " + normalizedMode + " ", " | ", strings.TrimSpace(hints)
}

// RenderStatusBarContract renders plain-text status bar content with deterministic width behavior.
func RenderStatusBarContract(contract StatusBarContract) string {
	modeSegment, separatorSegment, hintsSegment := StatusBarSegments(contract.Mode, contract.Hints)

	content := modeSegment
	if hintsSegment != "" {
		content += separatorSegment + hintsSegment
	}

	if contract.Width <= 0 {
		return content
	}

	if len(content) >= contract.Width {
		return content[:contract.Width]
	}

	return content + strings.Repeat(" ", contract.Width-len(content))
}
