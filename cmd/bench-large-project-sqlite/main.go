package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	_ "modernc.org/sqlite"
)

const (
	defaultProjectID = "proj-large-sqlite-harness"
	outputSchema     = "azedarach.large_project_sqlite_perf.v1"
)

type harnessConfig struct {
	DBPath          string
	ProjectID       string
	Iterations      int
	TaskCount       int
	RootCount       int
	ChildrenPerRoot int
	BlockerEvery    int
	WorktreeEvery   int
	ActiveEvery     int
	BatchSize       int
	IncludeSamples  bool
	Timeout         time.Duration
}

type fixtureSummary struct {
	DBPath           string `json:"db_path"`
	ProjectID        string `json:"project_id"`
	IssueCount       int    `json:"issue_count"`
	DependencyCount  int    `json:"dependency_count"`
	SessionCount     int    `json:"session_count"`
	WorktreeCount    int    `json:"worktree_count"`
	ExternalRefCount int    `json:"external_ref_count"`
	RootCount        int    `json:"root_count"`
	ChildrenPerRoot  int    `json:"children_per_root"`
	BatchSize        int    `json:"batch_size"`
}

type operationResult struct {
	Name        string      `json:"name"`
	Iterations  int         `json:"iterations"`
	OutputCount int         `json:"output_count"`
	Stats       timingStats `json:"stats"`
	Samples     []sample    `json:"samples,omitempty"`
}

type sample struct {
	DurationMs  float64 `json:"duration_ms"`
	OutputCount int     `json:"output_count"`
}

type timingStats struct {
	MinMs  float64 `json:"min_ms"`
	P50Ms  float64 `json:"p50_ms"`
	P95Ms  float64 `json:"p95_ms"`
	MaxMs  float64 `json:"max_ms"`
	MeanMs float64 `json:"mean_ms"`
}

type bottleneckRank struct {
	Rank   int     `json:"rank"`
	Name   string  `json:"name"`
	P95Ms  float64 `json:"p95_ms"`
	MeanMs float64 `json:"mean_ms"`
}

type queryPlan struct {
	Name string         `json:"name"`
	Rows []queryPlanRow `json:"rows"`
}

type queryPlanRow struct {
	ID     int    `json:"id"`
	Parent int    `json:"parent"`
	Detail string `json:"detail"`
}

type harnessResult struct {
	SchemaVersion string            `json:"schema_version"`
	StartedAt     time.Time         `json:"started_at"`
	FinishedAt    time.Time         `json:"finished_at"`
	Fixture       fixtureSummary    `json:"fixture"`
	Operations    []operationResult `json:"operations"`
	Bottlenecks   []bottleneckRank  `json:"bottlenecks"`
	QueryPlans    []queryPlan       `json:"query_plans"`
}

type fixtureIDs struct {
	RootID          string
	BatchIDs        []string
	MetadataIDs     []string
	CloseCandidates []string
}

type operation struct {
	name string
	run  func(context.Context, int) (int, error)
}

func main() {
	cfg := parseFlags()
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	result, err := runHarness(ctx, cfg)
	if err != nil {
		exitf("%v", err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		exitf("encode result: %v", err)
	}
}

func parseFlags() harnessConfig {
	cfg := harnessConfig{
		ProjectID:       defaultProjectID,
		Iterations:      5,
		TaskCount:       5000,
		RootCount:       6,
		ChildrenPerRoot: 80,
		BlockerEvery:    5,
		WorktreeEvery:   4,
		ActiveEvery:     11,
		BatchSize:       24,
		Timeout:         2 * time.Minute,
	}
	flag.StringVar(&cfg.DBPath, "db", "", "SQLite output path; defaults to a temporary database")
	flag.StringVar(&cfg.ProjectID, "project-id", cfg.ProjectID, "project id stored in runtime projections")
	flag.IntVar(&cfg.Iterations, "iterations", cfg.Iterations, "measurement iterations per operation")
	flag.IntVar(&cfg.TaskCount, "tasks", cfg.TaskCount, "synthetic non-root task count")
	flag.IntVar(&cfg.RootCount, "roots", cfg.RootCount, "root epic count")
	flag.IntVar(&cfg.ChildrenPerRoot, "children-per-root", cfg.ChildrenPerRoot, "parent-child tasks per root")
	flag.IntVar(&cfg.BlockerEvery, "blocker-every", cfg.BlockerEvery, "add a blocks edge for every Nth child (0 disables)")
	flag.IntVar(&cfg.WorktreeEvery, "worktree-every", cfg.WorktreeEvery, "add a worktree projection every Nth task (0 disables)")
	flag.IntVar(&cfg.ActiveEvery, "active-session-every", cfg.ActiveEvery, "mark every Nth session active instead of stopped (0 disables)")
	flag.IntVar(&cfg.BatchSize, "batch-size", cfg.BatchSize, "task id batch size for get-many and selector operations")
	flag.BoolVar(&cfg.IncludeSamples, "samples", false, "include raw per-iteration samples")
	flag.DurationVar(&cfg.Timeout, "timeout", cfg.Timeout, "overall harness timeout")
	flag.Parse()
	return cfg
}

func runHarness(ctx context.Context, cfg harnessConfig) (harnessResult, error) {
	if err := validateConfig(&cfg); err != nil {
		return harnessResult{}, err
	}
	dbPath, cleanup, err := prepareDBPath(cfg.DBPath)
	if err != nil {
		return harnessResult{}, err
	}
	defer cleanup()
	cfg.DBPath = dbPath

	started := time.Now().UTC()
	if err := initializeStore(ctx, dbPath); err != nil {
		return harnessResult{}, err
	}
	db, err := openHarnessDB(dbPath)
	if err != nil {
		return harnessResult{}, err
	}
	ids, fixture, err := seedFixture(ctx, db, cfg)
	if closeErr := db.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		return harnessResult{}, err
	}

	client := issues.NewClientAtPath(dbPath, discardLogger())
	defer client.CloseDB()
	directDB, err := openHarnessDB(dbPath)
	if err != nil {
		return harnessResult{}, err
	}
	defer directDB.Close()

	ops := []operation{
		{name: "task.list/list_summaries_with_runtime", run: func(ctx context.Context, _ int) (int, error) {
			tasks, err := client.ListSummariesWithRuntime(ctx, cfg.ProjectID)
			return len(tasks), err
		}},
		{name: "task.list/search_with_runtime", run: func(ctx context.Context, _ int) (int, error) {
			tasks, err := client.SearchWithRuntime(ctx, cfg.ProjectID, "large project task")
			return len(tasks), err
		}},
		{name: "task.get_many/dependency_context_runtime", run: func(ctx context.Context, _ int) (int, error) {
			tasks, err := client.GetManyWithDependencyContextRuntime(ctx, cfg.ProjectID, ids.BatchIDs)
			return len(tasks), err
		}},
		{name: "task.get_many/selector_metadata_ancestor_runtime", run: func(ctx context.Context, _ int) (int, error) {
			tasks, err := client.GetManyMetadataWithAncestorContextRuntime(ctx, cfg.ProjectID, ids.MetadataIDs)
			return len(tasks), err
		}},
		{name: "task.graph_readiness/list_graph_readiness_with_runtime", run: func(ctx context.Context, _ int) (int, error) {
			tasks, err := client.ListGraphReadinessWithRuntime(ctx, cfg.ProjectID, ids.RootID)
			return len(tasks), err
		}},
		{name: "task.close/preflight_update_close", run: func(ctx context.Context, iteration int) (int, error) {
			if iteration >= len(ids.CloseCandidates) {
				return 0, fmt.Errorf("missing close candidate for iteration %d", iteration)
			}
			if err := preflightCloseCandidate(ctx, directDB, ids.CloseCandidates[iteration]); err != nil {
				return 0, err
			}
			if err := client.Close(ctx, ids.CloseCandidates[iteration], ""); err != nil {
				return 0, err
			}
			return 1, nil
		}},
		{name: "runtime.worktree_projection_refresh/direct_projection_scan", run: func(ctx context.Context, _ int) (int, error) {
			return countWorktreeProjectionRows(ctx, directDB, cfg.ProjectID)
		}},
	}
	results := make([]operationResult, 0, len(ops))
	for _, op := range ops {
		result, err := measureOperation(ctx, cfg, op)
		if err != nil {
			return harnessResult{}, err
		}
		results = append(results, result)
	}

	plans, planErr := captureQueryPlans(ctx, directDB, cfg, ids)
	if planErr != nil {
		return harnessResult{}, planErr
	}

	return harnessResult{
		SchemaVersion: outputSchema,
		StartedAt:     started,
		FinishedAt:    time.Now().UTC(),
		Fixture:       fixture,
		Operations:    results,
		Bottlenecks:   rankBottlenecks(results, 3),
		QueryPlans:    plans,
	}, nil
}

func validateConfig(cfg *harnessConfig) error {
	if cfg == nil {
		return errors.New("nil config")
	}
	cfg.ProjectID = strings.TrimSpace(cfg.ProjectID)
	if cfg.ProjectID == "" {
		cfg.ProjectID = defaultProjectID
	}
	if cfg.Iterations < 1 {
		return errors.New("iterations must be >= 1")
	}
	if cfg.TaskCount < 1 {
		return errors.New("tasks must be >= 1")
	}
	if cfg.RootCount < 1 {
		return errors.New("roots must be >= 1")
	}
	if cfg.ChildrenPerRoot < 0 || cfg.BlockerEvery < 0 || cfg.WorktreeEvery < 0 || cfg.ActiveEvery < 0 {
		return errors.New("fixture frequency values must be >= 0")
	}
	if cfg.BatchSize < 1 {
		return errors.New("batch-size must be >= 1")
	}
	if cfg.Timeout <= 0 {
		return errors.New("timeout must be > 0")
	}
	return nil
}

func prepareDBPath(path string) (string, func(), error) {
	path = strings.TrimSpace(path)
	if path != "" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", nil, fmt.Errorf("resolve db path: %w", err)
		}
		if _, err := os.Stat(abs); err == nil {
			return "", nil, fmt.Errorf("db path already exists: %s", abs)
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", nil, fmt.Errorf("inspect db path: %w", err)
		}
		return abs, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "az-large-sqlite-*")
	if err != nil {
		return "", nil, fmt.Errorf("create temp dir: %w", err)
	}
	return filepath.Join(dir, "azedarach-large-project.db"), func() { _ = os.RemoveAll(dir) }, nil
}

func initializeStore(ctx context.Context, dbPath string) error {
	client := issues.NewClientAtPath(dbPath, discardLogger())
	defer client.CloseDB()
	_, err := client.List(ctx)
	return err
}

func openHarnessDB(dbPath string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)&_txlock=immediate", filepath.ToSlash(dbPath))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func seedFixture(ctx context.Context, db *sql.DB, cfg harnessConfig) (fixtureIDs, fixtureSummary, error) {
	now := "2026-01-01T00:00:00Z"
	closeCandidateCount := cfg.Iterations
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fixtureIDs{}, fixtureSummary{}, err
	}
	defer tx.Rollback()

	issueStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO issues (id, title, description, status, priority, issue_type, created_at, updated_at, labels_json, implementations_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fixtureIDs{}, fixtureSummary{}, err
	}
	defer issueStmt.Close()
	depStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO issue_dependencies (issue_id, depends_on_id, dependency_type)
		VALUES (?, ?, ?)
	`)
	if err != nil {
		return fixtureIDs{}, fixtureSummary{}, err
	}
	defer depStmt.Close()
	sessionStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO daemon_session_projections (project_id, session_id, issue_id, state, observed_state, activity, activity_source, tmux_attached_count, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fixtureIDs{}, fixtureSummary{}, err
	}
	defer sessionStmt.Close()
	worktreeStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO daemon_worktree_projections (project_id, issue_id, path, branch, updated_at, git_status_json, git_status_updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fixtureIDs{}, fixtureSummary{}, err
	}
	defer worktreeStmt.Close()
	refStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO issue_external_refs (issue_id, provider, provider_scope, remote_key, display_key, url, metadata_json, created_at, updated_at)
		VALUES (?, 'linear', 'team:PERF', ?, ?, ?, '{}', ?, ?)
	`)
	if err != nil {
		return fixtureIDs{}, fixtureSummary{}, err
	}
	defer refStmt.Close()

	ids := fixtureIDs{
		RootID:          rootID(0),
		BatchIDs:        make([]string, 0, cfg.BatchSize),
		MetadataIDs:     make([]string, 0, cfg.BatchSize),
		CloseCandidates: make([]string, 0, closeCandidateCount),
	}
	summary := fixtureSummary{
		DBPath:          cfg.DBPath,
		ProjectID:       cfg.ProjectID,
		RootCount:       cfg.RootCount,
		ChildrenPerRoot: cfg.ChildrenPerRoot,
		BatchSize:       cfg.BatchSize,
	}

	for i := 0; i < cfg.RootCount; i++ {
		id := rootID(i)
		if _, err := issueStmt.ExecContext(ctx, id, "Large project root "+strconv.Itoa(i), "Synthetic Azedarach root graph", string(domain.StatusInProgress), int(domain.P1), string(domain.TypeEpic), now, now, `["perf","root"]`, `["default"]`); err != nil {
			return fixtureIDs{}, fixtureSummary{}, err
		}
		summary.IssueCount++
	}

	for i := 0; i < cfg.TaskCount; i++ {
		id := taskID(i)
		status := domain.StatusOpen
		if i%13 == 0 {
			status = domain.StatusInReview
		} else if i%7 == 0 {
			status = domain.StatusInProgress
		}
		if _, err := issueStmt.ExecContext(ctx, id, "Large project task "+strconv.Itoa(i), "large project task body with workspace traces and selector context", string(status), int(domain.Priority(i%5)), string(domain.TypeTask), now, timestampForIndex(i), `["perf","synthetic"]`, `["default"]`); err != nil {
			return fixtureIDs{}, fixtureSummary{}, err
		}
		summary.IssueCount++
		if len(ids.BatchIDs) < cfg.BatchSize && i%97 == 0 {
			ids.BatchIDs = append(ids.BatchIDs, id)
		}
		childLimit := cfg.RootCount * cfg.ChildrenPerRoot
		if i < childLimit {
			parent := rootID(i / max(1, cfg.ChildrenPerRoot))
			if _, err := depStmt.ExecContext(ctx, id, parent, string(domain.DependencyParentChild)); err != nil {
				return fixtureIDs{}, fixtureSummary{}, err
			}
			summary.DependencyCount++
			if len(ids.MetadataIDs) < cfg.BatchSize && i%11 == 0 {
				ids.MetadataIDs = append(ids.MetadataIDs, id)
			}
			if cfg.BlockerEvery > 0 && i > 0 && i%cfg.BlockerEvery == 0 {
				blocker := taskID(i - 1)
				if blocker != id {
					if _, err := depStmt.ExecContext(ctx, id, blocker, string(domain.DependencyBlocks)); err != nil {
						return fixtureIDs{}, fixtureSummary{}, err
					}
					summary.DependencyCount++
				}
			}
		} else if i > 0 {
			if _, err := depStmt.ExecContext(ctx, id, taskID(i-1), string(domain.DependencyRelatedTo)); err != nil {
				return fixtureIDs{}, fixtureSummary{}, err
			}
			summary.DependencyCount++
		}
		state := "stopped"
		activity := ""
		source := ""
		attached := 0
		if cfg.ActiveEvery > 0 && i%cfg.ActiveEvery == 0 {
			state = "running"
			activity = "busy"
			source = "hook"
			attached = 1
		}
		if _, err := sessionStmt.ExecContext(ctx, cfg.ProjectID, "sess-"+id, id, state, "", activity, source, attached, now, timestampForIndex(i)); err != nil {
			return fixtureIDs{}, fixtureSummary{}, err
		}
		summary.SessionCount++
		if cfg.WorktreeEvery > 0 && i%cfg.WorktreeEvery == 0 {
			statusJSON := fmt.Sprintf(`{"git_ahead_count":%d,"git_behind_count":%d,"has_changes":%t,"git_additions":%d,"git_deletions":%d}`, i%9, i%4, i%3 == 0, i%17, i%5)
			if _, err := worktreeStmt.ExecContext(ctx, cfg.ProjectID, id, "/tmp/az-large/"+id, "riordan/"+id+"/perf", timestampForIndex(i), statusJSON, timestampForIndex(i)); err != nil {
				return fixtureIDs{}, fixtureSummary{}, err
			}
			summary.WorktreeCount++
		}
		if _, err := refStmt.ExecContext(ctx, id, "PERF-"+strconv.Itoa(i), "PERF-"+strconv.Itoa(i), "https://linear.local/PERF-"+strconv.Itoa(i), now, now); err != nil {
			return fixtureIDs{}, fixtureSummary{}, err
		}
		summary.ExternalRefCount++
	}
	for len(ids.BatchIDs) < cfg.BatchSize && len(ids.BatchIDs) < cfg.TaskCount {
		ids.BatchIDs = append(ids.BatchIDs, taskID(len(ids.BatchIDs)))
	}
	for len(ids.MetadataIDs) < cfg.BatchSize && len(ids.MetadataIDs) < cfg.TaskCount {
		ids.MetadataIDs = append(ids.MetadataIDs, taskID(len(ids.MetadataIDs)))
	}
	for i := 0; i < closeCandidateCount; i++ {
		id := "bench-close-" + fmt.Sprintf("%05d", i)
		if _, err := issueStmt.ExecContext(ctx, id, "Close candidate "+strconv.Itoa(i), "close preflight candidate", string(domain.StatusOpen), int(domain.P3), string(domain.TypeTask), now, timestampForIndex(i), `["perf","close"]`, `["default"]`); err != nil {
			return fixtureIDs{}, fixtureSummary{}, err
		}
		ids.CloseCandidates = append(ids.CloseCandidates, id)
		summary.IssueCount++
	}
	if err := rebuildGraphClosure(ctx, tx); err != nil {
		return fixtureIDs{}, fixtureSummary{}, err
	}
	if err := tx.Commit(); err != nil {
		return fixtureIDs{}, fixtureSummary{}, err
	}
	return ids, summary, nil
}

func rebuildGraphClosure(ctx context.Context, db execer) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM issue_graph_closure`); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO issue_graph_closure (project_id, ancestor_id, descendant_id, dependency_type, depth, updated_at)
		WITH RECURSIVE parent_edges(ancestor_id, descendant_id) AS (
			SELECT d.depends_on_id, d.issue_id
			FROM issue_dependencies d
			INNER JOIN issues ancestor ON ancestor.id = d.depends_on_id AND ancestor.deleted_at IS NULL
			INNER JOIN issues descendant ON descendant.id = d.issue_id AND descendant.deleted_at IS NULL
			WHERE d.tombstoned_at IS NULL AND d.dependency_type IN ('parent-child', 'parent_child')
		),
		closure(ancestor_id, descendant_id, depth, path) AS (
			SELECT ancestor_id, descendant_id, 1, ',' || ancestor_id || ',' || descendant_id || ','
			FROM parent_edges
			UNION ALL
			SELECT c.ancestor_id, e.descendant_id, c.depth + 1, c.path || e.descendant_id || ','
			FROM closure c
			INNER JOIN parent_edges e ON e.ancestor_id = c.descendant_id
			WHERE instr(c.path, ',' || e.descendant_id || ',') = 0
		)
		SELECT 'default', ancestor_id, descendant_id, 'parent-child', MIN(depth), ?
		FROM closure
		WHERE ancestor_id <> descendant_id
		GROUP BY ancestor_id, descendant_id
	`, "2026-01-01T00:00:00Z")
	return err
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func measureOperation(ctx context.Context, cfg harnessConfig, op operation) (operationResult, error) {
	samples := make([]sample, 0, cfg.Iterations)
	outputCount := 0
	for i := 0; i < cfg.Iterations; i++ {
		start := time.Now()
		count, err := op.run(ctx, i)
		elapsed := float64(time.Since(start).Microseconds()) / 1000.0
		if err != nil {
			return operationResult{}, fmt.Errorf("%s iteration %d: %w", op.name, i, err)
		}
		outputCount = count
		samples = append(samples, sample{DurationMs: elapsed, OutputCount: count})
	}
	result := operationResult{
		Name:        op.name,
		Iterations:  cfg.Iterations,
		OutputCount: outputCount,
		Stats:       summarize(samples),
	}
	if cfg.IncludeSamples {
		result.Samples = samples
	}
	return result, nil
}

func summarize(samples []sample) timingStats {
	if len(samples) == 0 {
		return timingStats{}
	}
	values := make([]float64, 0, len(samples))
	total := 0.0
	for _, sample := range samples {
		values = append(values, sample.DurationMs)
		total += sample.DurationMs
	}
	sort.Float64s(values)
	return timingStats{
		MinMs:  roundMillis(values[0]),
		P50Ms:  roundMillis(percentile(values, 0.50)),
		P95Ms:  roundMillis(percentile(values, 0.95)),
		MaxMs:  roundMillis(values[len(values)-1]),
		MeanMs: roundMillis(total / float64(len(values))),
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	pos := p * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return sorted[lo]
	}
	weight := pos - float64(lo)
	return sorted[lo]*(1-weight) + sorted[hi]*weight
}

func roundMillis(v float64) float64 {
	return math.Round(v*1000) / 1000
}

func rankBottlenecks(results []operationResult, limit int) []bottleneckRank {
	sorted := append([]operationResult(nil), results...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Stats.P95Ms == sorted[j].Stats.P95Ms {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].Stats.P95Ms > sorted[j].Stats.P95Ms
	})
	if limit > len(sorted) {
		limit = len(sorted)
	}
	ranks := make([]bottleneckRank, 0, limit)
	for i := 0; i < limit; i++ {
		ranks = append(ranks, bottleneckRank{
			Rank:   i + 1,
			Name:   sorted[i].Name,
			P95Ms:  sorted[i].Stats.P95Ms,
			MeanMs: sorted[i].Stats.MeanMs,
		})
	}
	return ranks
}

func countWorktreeProjectionRows(ctx context.Context, db *sql.DB, projectID string) (int, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT w.issue_id, w.path, w.branch, w.git_status_json, w.updated_at
		FROM daemon_worktree_projections w
		INNER JOIN issues i ON i.id = w.issue_id AND i.deleted_at IS NULL
		WHERE w.project_id = ?
		ORDER BY w.updated_at DESC, w.issue_id
	`, projectID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var issueID, path, branch, gitStatus, updated string
		if err := rows.Scan(&issueID, &path, &branch, &gitStatus, &updated); err != nil {
			return 0, err
		}
		count++
	}
	return count, rows.Err()
}

func preflightCloseCandidate(ctx context.Context, db *sql.DB, issueID string) error {
	var openChildCount int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(1)
		FROM issue_dependencies d
		JOIN issues child ON child.id = d.issue_id
		WHERE d.depends_on_id = ? AND d.tombstoned_at IS NULL AND d.dependency_type IN ('parent-child', 'parent_child') AND child.deleted_at IS NULL AND child.status != 'closed'
	`, issueID).Scan(&openChildCount); err != nil {
		return fmt.Errorf("close preflight open child guard: %w", err)
	}
	if openChildCount > 0 {
		return fmt.Errorf("close preflight failed: %s has %d open child issues", issueID, openChildCount)
	}
	var runtimeAttachmentCount int
	if err := db.QueryRowContext(ctx, `
		SELECT (
			CASE
				WHEN EXISTS (
					SELECT 1
					FROM daemon_worktree_projections
					WHERE issue_id = ? AND TRIM(COALESCE(path, '')) <> ''
				)
				THEN 1 ELSE 0
			END
		) + (
			CASE
				WHEN EXISTS (
					SELECT 1
					FROM (
						SELECT project_id, session_id, issue_id, state, observed_state, activity, activity_source, tmux_attached_count, started_at, updated_at FROM daemon_session_projections
						UNION ALL
						SELECT project_id, session_id, issue_id, state, observed_state, activity, activity_source, tmux_attached_count, started_at, updated_at FROM daemon_session_observations
					)
					WHERE issue_id = ? AND LOWER(TRIM(COALESCE(state, ''))) <> 'stopped'
				)
				THEN 1 ELSE 0
			END
		)
	`, issueID, issueID).Scan(&runtimeAttachmentCount); err != nil {
		return fmt.Errorf("close preflight runtime guard: %w", err)
	}
	if runtimeAttachmentCount > 0 {
		return fmt.Errorf("close preflight failed: %s has %d runtime attachments", issueID, runtimeAttachmentCount)
	}
	return nil
}

func captureQueryPlans(ctx context.Context, db *sql.DB, cfg harnessConfig, ids fixtureIDs) ([]queryPlan, error) {
	defs := []struct {
		name  string
		query string
		args  []any
	}{
		{name: "task.list/list_summaries_with_runtime", query: planTaskRuntimeProjectionQuery(false, false, len(ids.BatchIDs)), args: []any{cfg.ProjectID, cfg.ProjectID}},
		{name: "task.list/search_match_ids", query: `
			SELECT i.id
			FROM issue_search_fts
			JOIN issues i ON i.rowid = issue_search_fts.rowid
			WHERE issue_search_fts MATCH ? AND i.deleted_at IS NULL
			ORDER BY i.updated_at DESC, i.id
		`, args: []any{domain.ContentQueryFTSExpression("large project task")}},
		{name: "task.get_many/dependency_context_ids", query: planDependencyContextIDsQuery(len(ids.BatchIDs)), args: repeatArgs(ids.BatchIDs, 3)},
		{name: "task.get_many/selector_metadata_runtime", query: planMetadataRuntimeQuery(len(ids.MetadataIDs)), args: append(append([]any{cfg.ProjectID}, stringArgs(ids.MetadataIDs)...), append([]any{cfg.ProjectID, string(domain.DependencyParentChild), "parent_child"}, stringArgs(ids.MetadataIDs)...)...)},
		{name: "task.graph_readiness/context_ids", query: planGraphReadinessContextIDsQuery(), args: []any{ids.RootID, "default", string(domain.DependencyParentChild), ids.RootID}},
		{name: "task.close/open_child_guard", query: `
			SELECT COUNT(1)
			FROM issue_dependencies d
			JOIN issues child ON child.id = d.issue_id
			WHERE d.depends_on_id = ? AND d.tombstoned_at IS NULL AND d.dependency_type IN ('parent-child', 'parent_child') AND child.deleted_at IS NULL AND child.status != 'closed'
		`, args: []any{ids.CloseCandidates[0]}},
		{name: "task.close/runtime_attachment_guard", query: `
			SELECT (
				CASE WHEN EXISTS (SELECT 1 FROM daemon_worktree_projections WHERE issue_id = ? AND TRIM(COALESCE(path, '')) <> '') THEN 1 ELSE 0 END
			) + (
				CASE WHEN EXISTS (
					SELECT 1
					FROM (
						SELECT project_id, session_id, issue_id, state, observed_state, activity, activity_source, tmux_attached_count, started_at, updated_at FROM daemon_session_projections
						UNION ALL
						SELECT project_id, session_id, issue_id, state, observed_state, activity, activity_source, tmux_attached_count, started_at, updated_at FROM daemon_session_observations
					)
					WHERE issue_id = ? AND LOWER(TRIM(COALESCE(state, ''))) <> 'stopped'
				) THEN 1 ELSE 0 END
			)
		`, args: []any{ids.CloseCandidates[0], ids.CloseCandidates[0]}},
		{name: "runtime.worktree_projection_refresh/direct_projection_scan", query: `
			SELECT w.issue_id, w.path, w.branch, w.git_status_json, w.updated_at
			FROM daemon_worktree_projections w
			INNER JOIN issues i ON i.id = w.issue_id AND i.deleted_at IS NULL
			WHERE w.project_id = ?
			ORDER BY w.updated_at DESC, w.issue_id
		`, args: []any{cfg.ProjectID}},
	}
	plans := make([]queryPlan, 0, len(defs))
	for _, def := range defs {
		rows, err := explainQueryPlan(ctx, db, def.query, def.args...)
		if err != nil {
			return nil, fmt.Errorf("query plan %s: %w", def.name, err)
		}
		plans = append(plans, queryPlan{Name: def.name, Rows: rows})
	}
	return plans, nil
}

func explainQueryPlan(ctx context.Context, db *sql.DB, query string, args ...any) ([]queryPlanRow, error) {
	rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []queryPlanRow
	for rows.Next() {
		var row queryPlanRow
		var notUsed int
		if err := rows.Scan(&row.ID, &row.Parent, &notUsed, &row.Detail); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func planTaskRuntimeProjectionQuery(_ bool, filtered bool, idCount int) string {
	sessionFilter := ""
	originFilter := ""
	whereFilter := ""
	if filtered {
		placeholders := placeholders(idCount)
		sessionFilter = " AND issue_id IN (" + placeholders + ")"
		originFilter = " AND issue_id IN (" + placeholders + ")"
		whereFilter = " AND i.id IN (" + placeholders + ")"
	}
	return `
		WITH ranked_session AS (
			SELECT issue_id, COALESCE(NULLIF(TRIM(observed_state), ''), state) AS state, COALESCE(started_at, '') AS started_at, updated_at, session_id,
				COALESCE(activity, '') AS activity, COALESCE(activity_source, '') AS activity_source, COALESCE(tmux_attached_count, 0) AS tmux_attached_count,
				ROW_NUMBER() OVER (PARTITION BY issue_id ORDER BY CASE COALESCE(NULLIF(TRIM(observed_state), ''), state) WHEN 'running' THEN 0 WHEN 'attached' THEN 0 WHEN 'paused' THEN 1 WHEN 'starting' THEN 2 WHEN 'stopped' THEN 3 ELSE 4 END, updated_at DESC, session_id DESC) AS rn
			FROM (
				SELECT project_id, session_id, issue_id, state, observed_state, activity, activity_source, tmux_attached_count, started_at, updated_at FROM daemon_session_projections
				UNION ALL
				SELECT project_id, session_id, issue_id, state, observed_state, activity, activity_source, tmux_attached_count, started_at, updated_at FROM daemon_session_observations
			)
			WHERE project_id = ?` + sessionFilter + `
		),
		session_pick AS (SELECT issue_id, state, started_at, updated_at, activity, activity_source, tmux_attached_count FROM ranked_session WHERE rn = 1),
		origin_pick AS (SELECT issue_id, MIN(provider) AS provider FROM issue_external_refs WHERE deleted_at IS NULL` + originFilter + ` GROUP BY issue_id)
		SELECT i.id, i.title, '', '', '', '', COALESCE(i.assignee, ''), COALESCE(i.labels_json, '[]'), i.estimate, i.status, i.priority, i.issue_type,
			COALESCE(i.implementations_json, '[]'), i.created_at, i.updated_at, COALESCE(sp.state, ''), COALESCE(sp.started_at, ''), COALESCE(sp.updated_at, ''),
			COALESCE(sp.activity, ''), COALESCE(sp.activity_source, ''), COALESCE(sp.tmux_attached_count, 0), COALESCE(w.path, ''), COALESCE(w.git_status_json, ''),
			COALESCE(w.updated_at, ''), COALESCE(w.git_status_updated_at, ''), COALESCE(o.provider, '')
		FROM issues i
		LEFT JOIN session_pick sp ON sp.issue_id = i.id
		LEFT JOIN daemon_worktree_projections w ON w.project_id = ? AND w.issue_id = i.id
		LEFT JOIN origin_pick o ON o.issue_id = i.id
		WHERE i.deleted_at IS NULL` + whereFilter + `
		ORDER BY i.updated_at DESC
	`
}

func planDependencyContextIDsQuery(idCount int) string {
	ph := placeholders(idCount)
	return `
		SELECT DISTINCT id
		FROM (
			SELECT id FROM issues WHERE deleted_at IS NULL AND id IN (` + ph + `)
			UNION ALL
			SELECT depends_on_id AS id FROM issue_dependencies WHERE issue_id IN (` + ph + `) AND tombstoned_at IS NULL
			UNION ALL
			SELECT issue_id AS id FROM issue_dependencies WHERE depends_on_id IN (` + ph + `) AND tombstoned_at IS NULL
		)
	`
}

func planMetadataRuntimeQuery(idCount int) string {
	ph := placeholders(idCount)
	return `
		WITH ranked_session AS (
			SELECT issue_id, COALESCE(NULLIF(TRIM(observed_state), ''), state) AS state, COALESCE(started_at, '') AS started_at, updated_at, session_id,
				COALESCE(activity, '') AS activity, COALESCE(activity_source, '') AS activity_source, COALESCE(tmux_attached_count, 0) AS tmux_attached_count,
				ROW_NUMBER() OVER (PARTITION BY issue_id ORDER BY updated_at DESC, session_id DESC) AS rn
			FROM (
				SELECT project_id, session_id, issue_id, state, observed_state, activity, activity_source, tmux_attached_count, started_at, updated_at FROM daemon_session_projections
				UNION ALL
				SELECT project_id, session_id, issue_id, state, observed_state, activity, activity_source, tmux_attached_count, started_at, updated_at FROM daemon_session_observations
			)
			WHERE project_id = ? AND issue_id IN (` + ph + `)
		),
		session_pick AS (SELECT issue_id, state, started_at, updated_at, activity, activity_source, tmux_attached_count FROM ranked_session WHERE rn = 1)
		SELECT i.id, i.title, i.status, i.priority, i.issue_type, i.created_at, i.updated_at, COALESCE(sp.state, ''), COALESCE(sp.started_at, ''), COALESCE(sp.updated_at, ''),
			COALESCE(sp.activity, ''), COALESCE(sp.activity_source, ''), COALESCE(sp.tmux_attached_count, 0), COALESCE(w.path, ''), COALESCE(w.git_status_json, ''),
			COALESCE(w.updated_at, ''), COALESCE(w.git_status_updated_at, ''), COALESCE(parent.depends_on_id, '')
		FROM issues i
		LEFT JOIN session_pick sp ON sp.issue_id = i.id
		LEFT JOIN daemon_worktree_projections w ON w.project_id = ? AND w.issue_id = i.id
		LEFT JOIN issue_dependencies parent ON parent.issue_id = i.id AND parent.tombstoned_at IS NULL AND parent.dependency_type IN (?, ?)
		WHERE i.deleted_at IS NULL AND i.id IN (` + ph + `)
		ORDER BY i.updated_at DESC
	`
}

func planGraphReadinessContextIDsQuery() string {
	return `
		WITH graph(id) AS (
			SELECT id FROM issues WHERE id = ? AND deleted_at IS NULL
			UNION
			SELECT closure.descendant_id
			FROM issue_graph_closure closure INDEXED BY idx_issue_graph_closure_ancestor
			INNER JOIN issues child ON child.id = closure.descendant_id AND child.deleted_at IS NULL
			WHERE closure.project_id = ? AND closure.dependency_type = ? AND closure.ancestor_id = ?
		),
		context(id) AS (
			SELECT id FROM graph
			UNION
			SELECT dep.depends_on_id
			FROM graph graph_issue
			CROSS JOIN issue_dependencies dep INDEXED BY idx_dependencies_issue_active_type
			CROSS JOIN issues dep_issue
			WHERE dep.issue_id = graph_issue.id AND dep_issue.id = dep.depends_on_id AND dep_issue.deleted_at IS NULL AND dep.tombstoned_at IS NULL
		)
		SELECT id FROM context
	`
}

func repeatArgs(ids []string, repeats int) []any {
	args := make([]any, 0, len(ids)*repeats)
	for i := 0; i < repeats; i++ {
		for _, id := range ids {
			args = append(args, id)
		}
	}
	return args
}

func stringArgs(ids []string) []any {
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	return args
}

func placeholders(n int) string {
	if n < 1 {
		n = 1
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func rootID(i int) string {
	return "bench-root-" + fmt.Sprintf("%02d", i)
}

func taskID(i int) string {
	return "bench-task-" + fmt.Sprintf("%05d", i)
}

func timestampForIndex(i int) string {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func exitf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
