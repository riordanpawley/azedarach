package backup

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// RuntimeConfig controls runtime backup behavior for command paths.
type RuntimeConfig struct {
	Enabled              bool
	IntervalMinutes      int
	WriteCooldownSeconds int
	MaxBackups           int
	Directory            string
}

// Runtime applies stale-on-open and write-cooldown backup policies.
type Runtime struct {
	projectRoot     string
	canonicalDBPath string
	cfg             RuntimeConfig

	now      func() time.Time
	snapshot func(src, dst string) error
	warn     func(string)

	mu            sync.Mutex
	lastWarningAt *time.Time
}

// RuntimeOption customizes runtime behavior for deterministic tests.
type RuntimeOption func(*Runtime)

// WithClock overrides time source.
func WithClock(clock func() time.Time) RuntimeOption {
	return func(runtime *Runtime) {
		if clock != nil {
			runtime.now = clock
		}
	}
}

// WithSnapshotter overrides snapshot creation behavior.
func WithSnapshotter(snapshotter func(src, dst string) error) RuntimeOption {
	return func(runtime *Runtime) {
		if snapshotter != nil {
			runtime.snapshot = snapshotter
		}
	}
}

// WithWarningHandler overrides warning emission behavior.
func WithWarningHandler(handler func(message string)) RuntimeOption {
	return func(runtime *Runtime) {
		if handler != nil {
			runtime.warn = handler
		}
	}
}

// NewRuntime builds a runtime backup policy runner.
func NewRuntime(
	projectRoot string,
	canonicalDBPath string,
	cfg RuntimeConfig,
	options ...RuntimeOption,
) *Runtime {
	runtime := &Runtime{
		projectRoot:     projectRoot,
		canonicalDBPath: canonicalDBPath,
		cfg:             cfg,
		now:             func() time.Time { return time.Now().UTC() },
		snapshot:        snapshotFile,
		warn:            func(string) {},
	}

	for _, option := range options {
		if option != nil {
			option(runtime)
		}
	}

	return runtime
}

// OnOpen evaluates and runs stale-on-open backup policy.
func (runtime *Runtime) OnOpen() {
	if !runtime.cfg.Enabled {
		return
	}

	latestBackup, err := runtime.latestSuccessfulBackup()
	if err != nil {
		runtime.handleFailure(err)
		return
	}

	now := runtime.now().UTC()
	if !ShouldRunStaleOnOpen(now, latestBackup, runtime.cfg.IntervalMinutes) {
		return
	}

	if err := runtime.createBackup(now); err != nil {
		runtime.handleFailure(err)
	}
}

// OnMutationSuccess evaluates and runs post-mutation write-cooldown backup policy.
func (runtime *Runtime) OnMutationSuccess() {
	if !runtime.cfg.Enabled {
		return
	}

	latestBackup, err := runtime.latestSuccessfulBackup()
	if err != nil {
		runtime.handleFailure(err)
		return
	}

	now := runtime.now().UTC()
	if !ShouldRunWriteCooldown(now, latestBackup, runtime.cfg.WriteCooldownSeconds) {
		return
	}

	if err := runtime.createBackup(now); err != nil {
		runtime.handleFailure(err)
	}
}

func (runtime *Runtime) backupDirectory() string {
	if filepath.IsAbs(runtime.cfg.Directory) {
		return filepath.Clean(runtime.cfg.Directory)
	}
	root := runtime.projectRoot
	if root == "" {
		root = "."
	}
	return filepath.Join(root, runtime.cfg.Directory)
}

func (runtime *Runtime) latestSuccessfulBackup() (*time.Time, error) {
	directory := runtime.backupDirectory()
	entries, err := os.ReadDir(directory)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list backups in %s: %w", directory, err)
	}

	var latest *time.Time
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		timestamp, ok := ParseBackupFilenameTimestamp(entry.Name())
		if !ok {
			continue
		}
		if latest == nil || timestamp.After(*latest) {
			value := timestamp
			latest = &value
		}
	}
	return latest, nil
}

func (runtime *Runtime) createBackup(now time.Time) error {
	if runtime.canonicalDBPath == "" {
		return nil
	}

	if _, err := os.Stat(runtime.canonicalDBPath); err != nil {
		if os.IsNotExist(err) {
			// Skip silently when canonical DB does not exist yet.
			return nil
		}
		return fmt.Errorf("stat canonical db %s: %w", runtime.canonicalDBPath, err)
	}

	directory := runtime.backupDirectory()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("ensure backup directory %s: %w", directory, err)
	}

	filename := FormatBackupFilename(now)
	finalPath := filepath.Join(directory, filename)
	tempPath := finalPath + ".tmp-" + strconv.FormatInt(now.UnixNano(), 10)

	if err := runtime.snapshot(runtime.canonicalDBPath, tempPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("create backup snapshot: %w", err)
	}

	if err := os.Rename(tempPath, finalPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("atomic rename backup snapshot: %w", err)
	}

	if err := runtime.applyRetention(directory); err != nil {
		return err
	}

	return nil
}

func (runtime *Runtime) applyRetention(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read backup directory for retention: %w", err)
	}

	filenames := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		filenames = append(filenames, entry.Name())
	}

	for _, filename := range PlanRetentionPrune(filenames, runtime.cfg.MaxBackups) {
		path := filepath.Join(directory, filename)
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("prune retained backup %s: %w", filename, removeErr)
		}
	}
	return nil
}

func (runtime *Runtime) handleFailure(err error) {
	if err == nil {
		return
	}

	now := runtime.now().UTC()
	if runtime.shouldWarn(now) {
		runtime.warn(
			fmt.Sprintf(
				"Local backup attempt failed (non-blocking): %v. Check issueTracker.local.backups settings and filesystem permissions.",
				err,
			),
		)
	}
}

func (runtime *Runtime) shouldWarn(now time.Time) bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()

	if runtime.lastWarningAt == nil {
		copied := now
		runtime.lastWarningAt = &copied
		return true
	}

	if ShouldRunWriteCooldown(now, runtime.lastWarningAt, runtime.cfg.WriteCooldownSeconds) {
		copied := now
		runtime.lastWarningAt = &copied
		return true
	}

	return false
}

func snapshotFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open source db: %w", err)
	}
	defer func() {
		_ = input.Close()
	}()

	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create temp backup file: %w", err)
	}

	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return fmt.Errorf("copy canonical db into snapshot: %w", err)
	}

	if err := output.Sync(); err != nil {
		_ = output.Close()
		return fmt.Errorf("sync temp backup file: %w", err)
	}

	if err := output.Close(); err != nil {
		return fmt.Errorf("close temp backup file: %w", err)
	}

	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	return !os.IsNotExist(err)
}

func countBackupFiles(path string) (int, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if _, ok := ParseBackupFilenameTimestamp(entry.Name()); ok {
			count++
		}
	}
	return count, nil
}

func readDirNames(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&fs.ModeType != 0 {
			continue
		}
		names = append(names, entry.Name())
	}
	return names, nil
}
