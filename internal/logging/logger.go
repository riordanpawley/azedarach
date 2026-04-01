package logging

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// NewTextFileLogger returns a text slog logger that writes to path.
// If setup fails it falls back to stderr and emits a warning.
func NewTextFileLogger(path string, level slog.Leveler) *slog.Logger {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		logger := NewTextStreamLogger(os.Stderr, level)
		logger.Warn("failed to create log directory; falling back to stderr logger", "log_path", path, "error", err)
		return logger
	}
	logFile, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		logger := NewTextStreamLogger(os.Stderr, level)
		logger.Warn("failed to open log file; falling back to stderr logger", "log_path", path, "error", err)
		return logger
	}
	return slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: level}))
}

// NewTextStreamLogger returns a text slog logger that writes to w.
func NewTextStreamLogger(w io.Writer, level slog.Leveler) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}

// NewDiscardLogger returns a text slog logger that discards all output.
func NewDiscardLogger(level slog.Leveler) *slog.Logger {
	return NewTextStreamLogger(io.Discard, level)
}
