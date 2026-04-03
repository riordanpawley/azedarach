package logstream

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
)

// SourceSpec identifies one named log source and the file path it maps to.
type SourceSpec struct {
	Name string
	Path string
}

// Entry is one source-tagged log line.
type Entry struct {
	Source       string
	RawLine      string
	Timestamp    time.Time
	HasTimestamp bool
	sequence     int64
}

type followState struct {
	Source SourceSpec
	Offset int64
	Carry  string
}

var logTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006/01/02 15:04:05",
}

var daemonTimePrefixLayout = "2006/01/02 15:04:05"

// ReadLastMerged returns a merged/sorted view of the last maxLines from each source.
func ReadLastMerged(sources []SourceSpec, maxLines int) ([]Entry, error) {
	if maxLines < 1 {
		return nil, fmt.Errorf("max lines must be greater than 0")
	}
	entries := make([]Entry, 0, len(sources)*maxLines)
	var seq int64
	for _, source := range sources {
		lines, err := readLastLogLines(source.Path, maxLines)
		if err != nil {
			return nil, fmt.Errorf("read log file %s: %w", source.Path, err)
		}
		for _, line := range lines {
			entry := newEntry(source.Name, line, seq)
			entries = append(entries, entry)
			seq++
		}
	}
	sortEntries(entries)
	return entries, nil
}

// Follow polls files for new lines, emits merged/sorted lines, and exits on context cancellation.
func Follow(ctx context.Context, sources []SourceSpec, pollInterval time.Duration, emit func(Entry) error) error {
	if pollInterval <= 0 {
		return fmt.Errorf("poll interval must be greater than 0")
	}
	states := make([]*followState, 0, len(sources))
	for _, source := range sources {
		info, err := os.Stat(source.Path)
		if err != nil {
			return fmt.Errorf("inspect log file %s: %w", source.Path, err)
		}
		states = append(states, &followState{
			Source: source,
			Offset: info.Size(),
		})
	}

	var seq int64
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			batch := make([]Entry, 0, len(states)*4)
			for _, state := range states {
				newEntries, err := flushNewLogData(state, &seq)
				if err != nil {
					return err
				}
				batch = append(batch, newEntries...)
			}
			if len(batch) == 0 {
				continue
			}
			sortEntries(batch)
			for _, entry := range batch {
				if err := emit(entry); err != nil {
					return err
				}
			}
		}
	}
}

// FormatLine normalizes timestamp presentation and prefixes each line with the source.
func FormatLine(source, rawLine string, loc *time.Location) string {
	line := strings.TrimRight(rawLine, "\r")
	if ts, rest, ok := ExtractTimestamp(line); ok {
		displayLoc := loc
		if displayLoc == nil {
			displayLoc = time.Local
		}
		displayTime := ts.In(displayLoc).Format("2006-01-02 15:04:05 MST")
		if strings.TrimSpace(rest) == "" {
			return fmt.Sprintf("[%s] %s", source, displayTime)
		}
		return fmt.Sprintf("[%s] %s %s", source, displayTime, rest)
	}
	return fmt.Sprintf("[%s] %s", source, line)
}

// ExtractTimestamp parses known daemon/slog timestamp prefixes from one log line.
func ExtractTimestamp(line string) (time.Time, string, bool) {
	if len(line) >= len(daemonTimePrefixLayout) {
		prefix := line[:len(daemonTimePrefixLayout)]
		if ts, err := time.ParseInLocation(daemonTimePrefixLayout, prefix, time.Local); err == nil {
			rest := strings.TrimSpace(line[len(daemonTimePrefixLayout):])
			return ts, rest, true
		}
	}

	if !strings.HasPrefix(line, "time=") {
		return time.Time{}, "", false
	}
	spaceIdx := strings.IndexByte(line, ' ')
	timeField := line
	rest := ""
	if spaceIdx >= 0 {
		timeField = line[:spaceIdx]
		rest = strings.TrimSpace(line[spaceIdx+1:])
	}
	rawTS := strings.TrimPrefix(timeField, "time=")
	for _, layout := range logTimeLayouts {
		ts, err := time.Parse(layout, rawTS)
		if err == nil {
			return ts, rest, true
		}
	}
	return time.Time{}, "", false
}

func newEntry(source, rawLine string, seq int64) Entry {
	entry := Entry{
		Source:   source,
		RawLine:  strings.TrimRight(rawLine, "\r"),
		sequence: seq,
	}
	if ts, _, ok := ExtractTimestamp(entry.RawLine); ok {
		entry.Timestamp = ts
		entry.HasTimestamp = true
	}
	return entry
}

func sortEntries(entries []Entry) {
	slices.SortStableFunc(entries, func(a, b Entry) int {
		if a.HasTimestamp && b.HasTimestamp {
			if a.Timestamp.Before(b.Timestamp) {
				return -1
			}
			if a.Timestamp.After(b.Timestamp) {
				return 1
			}
		}
		if a.HasTimestamp != b.HasTimestamp {
			if a.HasTimestamp {
				return -1
			}
			return 1
		}
		switch {
		case a.sequence < b.sequence:
			return -1
		case a.sequence > b.sequence:
			return 1
		default:
			return 0
		}
	})
}

func readLastLogLines(path string, maxLines int) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	lines := make([]string, 0, maxLines)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > maxLines {
			lines = lines[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

func flushNewLogData(state *followState, seq *int64) ([]Entry, error) {
	info, err := os.Stat(state.Source.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			state.Offset = 0
			state.Carry = ""
			return nil, nil
		}
		return nil, fmt.Errorf("inspect log file %s: %w", state.Source.Path, err)
	}
	if info.Size() < state.Offset {
		state.Offset = 0
		state.Carry = ""
	}
	if info.Size() == state.Offset {
		return nil, nil
	}

	file, err := os.Open(state.Source.Path)
	if err != nil {
		return nil, fmt.Errorf("open log file %s: %w", state.Source.Path, err)
	}
	defer file.Close()
	if _, err := file.Seek(state.Offset, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek log file %s: %w", state.Source.Path, err)
	}

	out := make([]Entry, 0, 8)
	reader := bufio.NewReader(file)
	for {
		line, readErr := reader.ReadString('\n')
		if strings.HasSuffix(line, "\n") {
			fullLine := state.Carry + strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			state.Carry = ""
			out = append(out, newEntry(state.Source.Name, fullLine, *seq))
			(*seq)++
		} else {
			state.Carry += line
		}
		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		return nil, fmt.Errorf("read log file %s: %w", state.Source.Path, readErr)
	}

	state.Offset = info.Size()
	return out, nil
}
