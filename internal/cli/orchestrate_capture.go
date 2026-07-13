package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

const (
	compactCaptureMaxEvents      = 8
	compactCaptureMaxEventRunes  = 320
	compactCaptureMaxDetailRunes = 160
	compactCaptureEmptyOutput    = "No semantic transcript events found; rerun with --raw.\n"
)

type compactCapture struct {
	Output        string
	Events        int
	OmittedEvents int
}

type orchestrateCaptureResult struct {
	protocol.SessionCaptureResponseBody
	Mode          string `json:"mode"`
	RawBytes      int    `json:"raw_bytes"`
	OutputBytes   int    `json:"output_bytes"`
	Events        int    `json:"events"`
	OmittedEvents int    `json:"omitted_events"`
}

type captureEvent struct {
	marker     string
	text       string
	detail     string
	detailSeen bool
}

func orchestrateCaptureCommand(deps *Dependencies, opts SessionCaptureOptions) error {
	result, err := captureSessionPane(deps, opts, "capturing orchestration transcript")
	if err != nil {
		return err
	}
	rawBytes := len(result.Output)

	mode := "raw"
	output := result.Output
	compacted := compactCapture{}
	if !opts.Raw {
		mode = "compact"
		compacted = compactOrchestrationTranscript(result.Output)
		output = compacted.Output
	}
	result.Output = output

	if opts.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(orchestrateCaptureResult{
			SessionCaptureResponseBody: result,
			Mode:                       mode,
			RawBytes:                   rawBytes,
			OutputBytes:                len(output),
			Events:                     compacted.Events,
			OmittedEvents:              compacted.OmittedEvents,
		})
	}
	fmt.Print(output)
	if output != "" && !strings.HasSuffix(output, "\n") {
		fmt.Println()
	}
	return nil
}

func compactOrchestrationTranscript(input string) compactCapture {
	var events []captureEvent
	var current *captureEvent
	flush := func() {
		if current == nil || strings.TrimSpace(current.text) == "" {
			current = nil
			return
		}
		current.text = truncateRunes(strings.Join(strings.Fields(current.text), " "), compactCaptureMaxEventRunes)
		current.detail = truncateRunes(strings.Join(strings.Fields(current.detail), " "), compactCaptureMaxDetailRunes)
		events = append(events, *current)
		current = nil
	}

	for _, rawLine := range strings.Split(stripTerminalEscapes(input), "\n") {
		line := strings.TrimRight(rawLine, " \t\r")
		trimmed := strings.TrimSpace(line)
		marker, text, startsEvent := captureEventStart(trimmed)
		if startsEvent {
			flush()
			if captureEventNoise(text) {
				continue
			}
			current = &captureEvent{marker: marker, text: text}
			continue
		}
		if current == nil || trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "└ ") {
			if !current.detailSeen {
				current.detailSeen = true
				detail := strings.TrimSpace(strings.TrimPrefix(trimmed, "└ "))
				if !captureCollapsedLine(detail) {
					current.detail = detail
				}
			}
			continue
		}
		if strings.HasPrefix(trimmed, "│ ") && strings.HasPrefix(current.text, "Ran ") {
			current.text += " " + strings.TrimSpace(strings.TrimPrefix(trimmed, "│ "))
			continue
		}
		if captureChromeLine(trimmed) {
			flush()
			continue
		}
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') && !current.detailSeen {
			if !captureCollapsedLine(trimmed) {
				current.text += " " + trimmed
			}
		}
	}
	flush()

	omitted := 0
	if len(events) > compactCaptureMaxEvents {
		omitted = len(events) - compactCaptureMaxEvents
		events = events[omitted:]
	}
	var output strings.Builder
	if len(events) == 0 {
		output.WriteString(compactCaptureEmptyOutput)
	}
	for _, event := range events {
		output.WriteString(event.marker)
		output.WriteByte(' ')
		output.WriteString(event.text)
		if event.detail != "" {
			output.WriteString(" → ")
			output.WriteString(event.detail)
		}
		output.WriteByte('\n')
	}
	return compactCapture{Output: output.String(), Events: len(events), OmittedEvents: omitted}
}

func captureEventStart(line string) (marker, text string, ok bool) {
	for _, candidate := range []string{"•", "⚠", "●", "⏺"} {
		if strings.HasPrefix(line, candidate+" ") {
			return candidate, strings.TrimSpace(strings.TrimPrefix(line, candidate)), true
		}
	}
	return "", "", false
}

func captureEventNoise(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	return strings.HasPrefix(lower, "working (") ||
		strings.HasPrefix(lower, "running pretooluse hook") ||
		strings.HasPrefix(lower, "sessionstart hook") ||
		strings.HasPrefix(lower, "queued follow-up input") ||
		(strings.HasPrefix(lower, "you have ") && strings.Contains(lower, "usage limit"))
}

func captureChromeLine(line string) bool {
	return strings.HasPrefix(line, "›") ||
		strings.HasPrefix(line, "╭") || strings.HasPrefix(line, "╰") ||
		strings.HasPrefix(line, "├") || strings.HasPrefix(line, "└") ||
		strings.HasPrefix(line, "─") ||
		strings.HasPrefix(line, "gpt-") ||
		strings.Contains(line, "Context ") && strings.Contains(line, " left") ||
		strings.Contains(line, "esc to interrupt")
}

func captureCollapsedLine(line string) bool {
	return strings.HasPrefix(line, "… +") && strings.Contains(line, "lines")
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}

func stripTerminalEscapes(value string) string {
	var output strings.Builder
	output.Grow(len(value))
	for index := 0; index < len(value); {
		if value[index] != 0x1b {
			output.WriteByte(value[index])
			index++
			continue
		}
		index++
		if index >= len(value) || value[index] != '[' {
			continue
		}
		index++
		for index < len(value) {
			byteValue := value[index]
			index++
			if byteValue >= 0x40 && byteValue <= 0x7e {
				break
			}
		}
	}
	return output.String()
}
