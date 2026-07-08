package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/latencytrace"
	"github.com/riordanpawley/azedarach/internal/naming"
)

const (
	defaultScheduledScriptTimeout = 5 * time.Minute
	scheduledScriptScanInterval   = time.Second
	maxScheduledScriptOutputBytes = 16 * 1024
)

type scheduledScriptCommandRunner interface {
	Run(ctx context.Context, shell, command, cwd string, env []string) ([]byte, error)
}

type execScheduledScriptRunner struct{}

func (execScheduledScriptRunner) Run(ctx context.Context, shell, command, cwd string, env []string) ([]byte, error) {
	ctx, endSpan := latencytrace.StartSpan(ctx, "dependency", "scheduled_script",
		"dependency.name", filepath.Base(shell),
		"dependency.operation", "run",
		"arg_count", 2,
	)
	cmd := exec.CommandContext(ctx, shell, "-lc", command)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	endSpan(err)
	return out, err
}

type scheduledScriptManager struct {
	daemon *Daemon
	logger *slog.Logger
	runner scheduledScriptCommandRunner

	mu        sync.Mutex
	statuses  map[string]map[string]*scheduledScriptRuntime
	closeCh   chan struct{}
	closeOnce sync.Once
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

type scheduledScriptRuntime struct {
	projectID string
	config    appconfig.ScheduledScriptConfig
	rootPath  string
	cwd       string
	interval  time.Duration

	running        int
	nextRunAt      *time.Time
	lastStartedAt  *time.Time
	lastFinishedAt *time.Time
	lastDuration   time.Duration
	lastExitCode   int
	lastError      string
	lastOutput     string
	lastLogPath    string
	runCount       int
	skipCount      int
}

func newScheduledScriptManager(d *Daemon, logger *slog.Logger, runner scheduledScriptCommandRunner) *scheduledScriptManager {
	if runner == nil {
		runner = execScheduledScriptRunner{}
	}
	return &scheduledScriptManager{
		daemon:   d,
		logger:   logger,
		runner:   runner,
		statuses: map[string]map[string]*scheduledScriptRuntime{},
		closeCh:  make(chan struct{}),
	}
}

func (d *Daemon) startScheduledScriptWorker(ctx context.Context) {
	if d == nil || d.scheduledScripts == nil {
		return
	}
	projectID := d.canonicalProjectID("")
	if strings.TrimSpace(projectID) == "" {
		projectID = protocol.DefaultProjectID
	}
	d.scheduledScripts.RegisterProject(projectID, timeNow())
	if !d.cfg.ScopedRuntime {
		if registry, err := appconfig.LoadProjectsRegistry(); err == nil && registry != nil {
			for _, project := range registry.Projects {
				if strings.TrimSpace(project.Name) == "" {
					continue
				}
				d.scheduledScripts.RegisterProject(project.Name, timeNow())
			}
		}
	}
	d.scheduledScripts.Start(ctx)
}

func (m *scheduledScriptManager) Start(ctx context.Context) {
	if m == nil {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(scheduledScriptScanInterval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-m.closeCh:
				return
			case now := <-ticker.C:
				m.RunDue(runCtx, now.UTC())
			}
		}
	}()
}

func (m *scheduledScriptManager) Close() {
	if m == nil {
		return
	}
	m.closeOnce.Do(func() {
		m.mu.Lock()
		cancel := m.cancel
		m.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		close(m.closeCh)
	})
	m.wg.Wait()
}

func (m *scheduledScriptManager) RegisterProject(projectID string, now time.Time) {
	if m == nil || m.daemon == nil {
		return
	}
	projectID = m.daemon.canonicalProjectID(projectID)
	cfg := m.daemon.runtimeConfigForProject(projectID).ScheduledScripts
	rootPath := strings.TrimSpace(m.daemon.resolveRepoDirForProjectExact(projectID))
	if rootPath == "" {
		rootPath = strings.TrimSpace(m.daemon.resolveRepoDirForProject(projectID))
	}
	if rootPath == "" {
		rootPath = strings.TrimSpace(m.daemon.cfg.RepoDir)
	}
	now = now.UTC()

	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.statuses[projectID]
	if current == nil {
		current = map[string]*scheduledScriptRuntime{}
		m.statuses[projectID] = current
	}
	seen := map[string]struct{}{}
	for _, script := range cfg.Scripts {
		name := normalizeScheduledScriptName(script.Name)
		if name == "" {
			name = normalizeScheduledScriptName(script.Command)
		}
		if name == "" {
			continue
		}
		seen[name] = struct{}{}
		interval, _ := scheduledScriptInterval(script)
		cwd := scheduledScriptCWD(rootPath, script.CWD)
		runtime := current[name]
		if runtime == nil {
			runtime = &scheduledScriptRuntime{
				projectID: projectID,
				nextRunAt: scheduledScriptNextRun(now, interval, script.Enabled),
			}
			current[name] = runtime
		}
		runtime.config = script
		runtime.config.Name = name
		runtime.rootPath = rootPath
		runtime.cwd = cwd
		runtime.interval = interval
		if runtime.nextRunAt == nil || !script.Enabled || interval <= 0 {
			runtime.nextRunAt = scheduledScriptNextRun(now, interval, script.Enabled)
		}
	}
	for name := range current {
		if _, ok := seen[name]; !ok {
			delete(current, name)
		}
	}
}

func (m *scheduledScriptManager) RunDue(ctx context.Context, now time.Time) {
	if m == nil {
		return
	}
	now = now.UTC()
	m.mu.Lock()
	var due []*scheduledScriptRuntime
	for _, byName := range m.statuses {
		for _, status := range byName {
			if !status.config.Enabled || status.interval <= 0 || status.nextRunAt == nil || now.Before(*status.nextRunAt) {
				continue
			}
			if status.running > 0 && !status.config.AllowOverlap {
				status.skipCount++
				next := now.Add(status.interval)
				status.nextRunAt = &next
				continue
			}
			status.running++
			startedAt := now
			status.lastStartedAt = &startedAt
			next := now.Add(status.interval)
			status.nextRunAt = &next
			copy := *status
			due = append(due, &copy)
		}
	}
	m.mu.Unlock()

	for _, status := range due {
		m.wg.Add(1)
		go func(status *scheduledScriptRuntime) {
			defer m.wg.Done()
			m.runOne(ctx, status)
		}(status)
	}
}

func (m *scheduledScriptManager) runOne(parent context.Context, status *scheduledScriptRuntime) {
	if m == nil || status == nil {
		return
	}
	timeout := scheduledScriptTimeout(status.config)
	ctx := parent
	cancel := func() {}
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, timeout)
	}
	defer cancel()

	startedAt := timeNow().UTC()
	output, err := m.runner.Run(ctx, scheduledScriptShell(m.daemon, status.projectID), status.config.Command, status.cwd, m.env(status))
	finishedAt := timeNow().UTC()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	exitCode := scheduledScriptExitCode(err)
	outputText := strings.TrimSpace(string(output))
	if len(outputText) > maxScheduledScriptOutputBytes {
		outputText = outputText[len(outputText)-maxScheduledScriptOutputBytes:]
	}
	logPath := m.writeLog(status, startedAt, finishedAt, exitCode, output, err)
	if m.logger != nil {
		attrs := []any{
			"project_id", status.projectID,
			"script", status.config.Name,
			"exit_code", exitCode,
			"duration_ms", finishedAt.Sub(startedAt).Milliseconds(),
			"log_path", logPath,
		}
		if err != nil {
			m.logger.Warn("scheduled script failed", append(attrs, "error", err)...)
		} else {
			m.logger.Info("scheduled script completed", attrs...)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.statuses[status.projectID][status.config.Name]
	if current == nil {
		return
	}
	if current.running > 0 {
		current.running--
	}
	current.lastStartedAt = &startedAt
	current.lastFinishedAt = &finishedAt
	current.lastDuration = finishedAt.Sub(startedAt)
	current.lastExitCode = exitCode
	current.lastError = scheduledScriptErrorString(err)
	current.lastOutput = outputText
	current.lastLogPath = logPath
	current.runCount++
}

func (m *scheduledScriptManager) env(status *scheduledScriptRuntime) []string {
	values := map[string]string{
		"AZEDARACH_PROJECT_ID":            status.projectID,
		"AZEDARACH_ROOT_PATH":             status.rootPath,
		"AZEDARACH_SCHEDULED_SCRIPT_NAME": status.config.Name,
		"AZEDARACH_SCRIPT_NAME":           status.config.Name,
	}
	projectCfg := m.daemon.runtimeConfigForProject(status.projectID).ScheduledScripts
	mergeScheduledScriptEnv(values, projectCfg.Env)
	mergeScheduledScriptEnv(values, status.config.Env)
	for range 2 {
		for key, value := range values {
			values[key] = os.Expand(value, func(name string) string {
				return values[name]
			})
		}
	}
	values["AZEDARACH_PROJECT_ID"] = status.projectID
	values["AZEDARACH_ROOT_PATH"] = status.rootPath
	values["AZEDARACH_SCHEDULED_SCRIPT_NAME"] = status.config.Name
	values["AZEDARACH_SCRIPT_NAME"] = status.config.Name
	env := make([]string, 0, len(values))
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	sort.Strings(env)
	return env
}

func mergeScheduledScriptEnv(values map[string]string, env map[string]string) {
	for key, value := range env {
		key = strings.TrimSpace(key)
		if !validShellEnvName(key) || strings.HasPrefix(key, "AZEDARACH_") {
			continue
		}
		values[key] = value
	}
}

func (m *scheduledScriptManager) writeLog(status *scheduledScriptRuntime, startedAt, finishedAt time.Time, exitCode int, output []byte, runErr error) string {
	if m == nil || status == nil || m.daemon == nil {
		return ""
	}
	logRoot := strings.TrimSpace(status.rootPath)
	if logRoot == "" {
		logRoot = strings.TrimSpace(m.daemon.cfg.RepoDir)
	}
	if logRoot == "" {
		logRoot = "."
	}
	logDir := filepath.Join(logRoot, ".azedarach", "scheduled-scripts", status.projectID)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return ""
	}
	logPath := filepath.Join(logDir, safeScheduledScriptFileName(status.config.Name)+".log")
	var b bytes.Buffer
	fmt.Fprintf(&b, "started_at=%s\nfinished_at=%s\nproject_id=%s\nscript=%s\ncwd=%s\ncommand=%s\nexit_code=%d\n",
		startedAt.Format(time.RFC3339Nano),
		finishedAt.Format(time.RFC3339Nano),
		status.projectID,
		status.config.Name,
		status.cwd,
		status.config.Command,
		exitCode,
	)
	if runErr != nil {
		fmt.Fprintf(&b, "error=%s\n", runErr)
	}
	b.WriteString("\n")
	b.Write(output)
	if len(output) == 0 || output[len(output)-1] != '\n' {
		b.WriteByte('\n')
	}
	if err := os.WriteFile(logPath, b.Bytes(), 0o644); err != nil {
		return ""
	}
	return logPath
}

func (m *scheduledScriptManager) Status(projectID string, names []string) protocol.ScheduledScriptsStatusResponseBody {
	if m == nil || m.daemon == nil {
		return protocol.ScheduledScriptsStatusResponseBody{
			ProjectID: naming.ProjectID(protocol.NormalizeProjectID(projectID)),
			Scripts:   []protocol.ScheduledScriptStatus{},
		}
	}
	projectID = m.daemon.canonicalProjectID(projectID)
	m.RegisterProject(projectID, timeNow())
	filter := map[string]struct{}{}
	for _, name := range names {
		if name = normalizeScheduledScriptName(name); name != "" {
			filter[name] = struct{}{}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	byName := m.statuses[projectID]
	out := make([]protocol.ScheduledScriptStatus, 0, len(byName))
	for name, status := range byName {
		if len(filter) > 0 {
			if _, ok := filter[name]; !ok {
				continue
			}
		}
		out = append(out, status.protocolStatus())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return protocol.ScheduledScriptsStatusResponseBody{
		ProjectID: naming.ProjectID(projectID),
		Scripts:   out,
	}
}

func (s *scheduledScriptRuntime) protocolStatus() protocol.ScheduledScriptStatus {
	timeout := scheduledScriptTimeout(s.config)
	return protocol.ScheduledScriptStatus{
		Name:           s.config.Name,
		Command:        s.config.Command,
		Enabled:        s.config.Enabled,
		Running:        s.running > 0,
		AllowOverlap:   s.config.AllowOverlap,
		CWD:            s.cwd,
		Interval:       s.interval.String(),
		Schedule:       s.config.Schedule,
		TimeoutMs:      int(timeout / time.Millisecond),
		NextRunAt:      cloneTimePtr(s.nextRunAt),
		LastStartedAt:  cloneTimePtr(s.lastStartedAt),
		LastFinishedAt: cloneTimePtr(s.lastFinishedAt),
		LastDurationMs: s.lastDuration.Milliseconds(),
		LastExitCode:   s.lastExitCode,
		LastError:      s.lastError,
		LastOutput:     s.lastOutput,
		LastLogPath:    s.lastLogPath,
		RunCount:       s.runCount,
		SkipCount:      s.skipCount,
	}
}

func (d *Daemon) handleScheduledScriptsStatus(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var cmd protocol.ScheduledScriptsStatusRequestBody
	if len(req.Body) > 0 {
		if err := json.Unmarshal(req.Body, &cmd); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
		}
	}
	projectID := d.projectID(req.Meta)
	if bodyProjectID := protocol.TrimProjectID(cmd.ProjectID.String()); bodyProjectID != "" {
		projectID = bodyProjectID
	}
	if d.scheduledScripts == nil {
		d.scheduledScripts = newScheduledScriptManager(d, d.cfg.Logger, d.cfg.scheduledScriptRunner)
	}
	result := d.scheduledScripts.Status(projectID, cmd.Names)
	body, err := json.Marshal(result)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	resp.Revision = d.currentRevision(projectID)
	return resp, nil
}

func scheduledScriptInterval(script appconfig.ScheduledScriptConfig) (time.Duration, error) {
	value := strings.TrimSpace(script.Interval)
	if value == "" {
		schedule := strings.TrimSpace(script.Schedule)
		if strings.HasPrefix(schedule, "@every ") {
			value = strings.TrimSpace(strings.TrimPrefix(schedule, "@every "))
		}
	}
	if value == "" {
		return 0, nil
	}
	interval, err := time.ParseDuration(value)
	if err != nil || interval <= 0 {
		return 0, err
	}
	return interval, nil
}

func scheduledScriptTimeout(script appconfig.ScheduledScriptConfig) time.Duration {
	if script.TimeoutMs <= 0 {
		return defaultScheduledScriptTimeout
	}
	return time.Duration(script.TimeoutMs) * time.Millisecond
}

func scheduledScriptNextRun(now time.Time, interval time.Duration, enabled bool) *time.Time {
	if !enabled || interval <= 0 {
		return nil
	}
	next := now.UTC().Add(interval)
	return &next
}

func scheduledScriptCWD(rootPath, configured string) string {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return strings.TrimSpace(rootPath)
	}
	if filepath.IsAbs(configured) {
		return filepath.Clean(configured)
	}
	return filepath.Join(strings.TrimSpace(rootPath), configured)
}

func scheduledScriptShell(d *Daemon, projectID string) string {
	if d != nil {
		shell := strings.TrimSpace(d.runtimeConfigForProject(projectID).SessionShell)
		if shell != "" {
			return shell
		}
	}
	return appconfig.DefaultSessionShell()
}

func scheduledScriptExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return -1
	}
	return 1
}

func scheduledScriptErrorString(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return strings.TrimSpace(err.Error())
}

func normalizeScheduledScriptName(name string) string {
	return strings.TrimSpace(name)
}

func safeScheduledScriptFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "script"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-',
			r == '_',
			r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "._-")
	if out == "" {
		return "script"
	}
	return out
}

func cloneTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	clone := t.UTC()
	return &clone
}
