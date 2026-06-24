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
	Info   os.FileInfo
}

var logTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006/01/02 15:04:05",
}

var daemonTimePrefixLayout = "2006/01/02 15:04:05"

const (
	tailReadChunkBytes = 32 * 1024
	maxTailReadBytes   = 4 * 1024 * 1024
)

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
			Info:   info,
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

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() == 0 {
		return nil, nil
	}

	var data []byte
	readBytes := int64(0)
	offset := info.Size()
	newlines := 0
	for offset > 0 && readBytes < maxTailReadBytes && newlines <= maxLines {
		chunkSize := int64(tailReadChunkBytes)
		if chunkSize > offset {
			chunkSize = offset
		}
		remainingBudget := maxTailReadBytes - readBytes
		if chunkSize > remainingBudget {
			chunkSize = remainingBudget
		}
		offset -= chunkSize
		chunk := make([]byte, int(chunkSize))
		if _, err := file.ReadAt(chunk, offset); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		data = append(chunk, data...)
		readBytes += chunkSize
		newlines += countNewlines(chunk)
	}
	text := strings.TrimRight(string(data), "\n")
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	if offset > 0 && len(lines) > 1 {
		lines = lines[1:]
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return lines, nil
}

func countNewlines(data []byte) int {
	count := 0
	for _, b := range data {
		if b == '\n' {
			count++
		}
	}
	return count
}

func flushNewLogData(state *followState, seq *int64) ([]Entry, error) {
	info, err := os.Stat(state.Source.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			state.Offset = 0
			state.Carry = ""
			state.Info = nil
			return nil, nil
		}
		return nil, fmt.Errorf("inspect log file %s: %w", state.Source.Path, err)
	}
	if state.Info != nil && !os.SameFile(info, state.Info) {
		state.Offset = 0
		state.Carry = ""
	}
	if info.Size() < state.Offset {
		state.Offset = 0
		state.Carry = ""
	}
	if info.Size() == state.Offset {
		state.Info = info
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
	state.Info = info
	return out, nil
}
