package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// RotatingFile writes to one active file and keeps numbered backups.
type RotatingFile struct {
	mu         sync.Mutex
	path       string
	maxBytes   int64
	maxBackups int
	file       *os.File
	size       int64
}

// OpenRotatingFile opens path for append, rotating it when it reaches maxBytes.
func OpenRotatingFile(path string, maxBytes int64, maxBackups int) (*RotatingFile, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("max bytes must be greater than 0")
	}
	if maxBackups < 0 {
		return nil, fmt.Errorf("max backups must be non-negative")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	r := &RotatingFile{
		path:       path,
		maxBytes:   maxBytes,
		maxBackups: maxBackups,
	}
	if err := r.open(); err != nil {
		return nil, err
	}
	if r.size >= r.maxBytes {
		if err := r.rotateLocked(); err != nil {
			if r.file != nil {
				_ = r.file.Close()
			}
			return nil, err
		}
	}
	return r, nil
}

// Write appends p to the active file, rotating before the write when needed.
func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	if r.size > 0 && r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := r.file.Write(p)
	r.size += int64(n)
	return n, err
}

// Close closes the active file.
func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

func (r *RotatingFile) open() error {
	file, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("inspect log file: %w", err)
	}
	r.file = file
	r.size = info.Size()
	return nil
}

func (r *RotatingFile) rotateLocked() error {
	if r.file != nil {
		if err := r.file.Close(); err != nil {
			return fmt.Errorf("close log before rotate: %w", err)
		}
		r.file = nil
	}
	if r.maxBackups == 0 {
		if err := os.Remove(r.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove current log: %w", err)
		}
		return r.open()
	}
	oldest := backupPath(r.path, r.maxBackups)
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove oldest log backup: %w", err)
	}
	for i := r.maxBackups - 1; i >= 1; i-- {
		src := backupPath(r.path, i)
		dst := backupPath(r.path, i+1)
		if err := os.Rename(src, dst); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("rotate log backup %s: %w", src, err)
		}
	}
	if err := os.Rename(r.path, backupPath(r.path, 1)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("rotate current log: %w", err)
	}
	return r.open()
}

func backupPath(path string, index int) string {
	return fmt.Sprintf("%s.%d", path, index)
}
