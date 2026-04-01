package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

var (
	daemonDiffStatInsertionsPattern = regexp.MustCompile(`(\d+)\s+insertion(?:s)?\(\+\)`)
	daemonDiffStatDeletionsPattern  = regexp.MustCompile(`(\d+)\s+deletion(?:s)?\(-\)`)
)

const runtimeSignalProjectionTTL = 15 * time.Second

type gitServiceAdapter struct {
	client          *git.Client
	projectionStore *daemonstate.ProjectionStore
	logger          *slog.Logger
	pollInterval    time.Duration
	onStatusUpdate  func(projectID, issueID, worktree string)
	baseBranch      string

	refreshMu      sync.Mutex
	refreshRunning map[string]bool
	pollers        map[string]context.CancelFunc

	runtimeSignalsMu    sync.Mutex
	runtimeSignalsCache map[string]runtimeSignalProjection
}

type runtimeSignalProjection struct {
	signal      daemonhandlers.GitRuntimeSignalsResult
	refreshedAt time.Time
}

func (a *gitServiceAdapter) Fetch(ctx context.Context, projectID, worktree, remote string) error {
	if err := a.client.Fetch(ctx, worktree, remote); err != nil {
		return err
	}
	a.refreshGitStatusWriteThrough(ctx, projectID, worktree, true, false)
	return nil
}

func (a *gitServiceAdapter) Merge(ctx context.Context, projectID, worktree, branch string) (*git.MergeResult, error) {
	result, err := a.client.Merge(ctx, worktree, branch)
	if err != nil {
		return nil, err
	}
	// Merge completion should always trigger an update notification so clients
	// refresh runtime git signals even when porcelain status stays clean.
	a.refreshGitStatusWriteThrough(ctx, projectID, worktree, true, true)
	return result, nil
}

func (a *gitServiceAdapter) Checkout(ctx context.Context, projectID, worktree, branch string) error {
	if err := a.client.Checkout(ctx, worktree, branch); err != nil {
		return err
	}
	a.refreshGitStatusWriteThrough(ctx, projectID, worktree, true, false)
	return nil
}

func (a *gitServiceAdapter) AbortMerge(ctx context.Context, projectID, worktree string) error {
	if err := a.client.AbortMerge(ctx, worktree); err != nil {
		return err
	}
	a.refreshGitStatusWriteThrough(ctx, projectID, worktree, true, false)
	return nil
}

func (a *gitServiceAdapter) DiffStat(ctx context.Context, _ string, worktree, baseBranch string) (string, error) {
	return a.client.DiffStat(ctx, worktree, baseBranch)
}

func (a *gitServiceAdapter) RuntimeSignals(ctx context.Context, projectID string, targets []daemonhandlers.GitRuntimeSignalsTarget, baseBranch string, compareRemote bool, remote string) ([]daemonhandlers.GitRuntimeSignalsResult, int, error) {
	projectID = normalizeProjectID(projectID)
	baseBranch = strings.TrimSpace(baseBranch)
	if baseBranch == "" {
		baseBranch = strings.TrimSpace(a.baseBranch)
	}
	if baseBranch == "" {
		baseBranch = "main"
	}
	remote = strings.TrimSpace(remote)
	if remote == "" {
		remote = "origin"
	}

	results := make([]daemonhandlers.GitRuntimeSignalsResult, 0, len(targets))
	partialFailures := 0
	now := time.Now()
	for _, target := range targets {
		issueID := strings.TrimSpace(target.IssueID)
		worktree := strings.TrimSpace(target.Worktree)
		if issueID == "" || worktree == "" {
			continue
		}
		cacheKey := runtimeSignalCacheKey(projectID, issueID, worktree, baseBranch, compareRemote, remote)
		if cached, ok := a.cachedRuntimeSignal(cacheKey, now); ok {
			results = append(results, cached)
			continue
		}
		signal, err := a.computeRuntimeSignal(ctx, issueID, worktree, baseBranch, compareRemote, remote)
		if err != nil {
			partialFailures++
			if cached, ok := a.cachedRuntimeSignalAnyBase(projectID, issueID, worktree, now); ok {
				results = append(results, cached)
				continue
			}
			results = append(results, daemonhandlers.GitRuntimeSignalsResult{
				IssueID:  issueID,
				Worktree: worktree,
			})
			continue
		}
		a.storeRuntimeSignal(cacheKey, signal, now)
		results = append(results, signal)
	}

	return results, partialFailures, nil
}

func (a *gitServiceAdapter) Status(ctx context.Context, projectID, worktree string) (*git.GitStatus, error) {
	projectID = normalizeProjectID(projectID)
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return a.client.Status(ctx, worktree)
	}
	if a.projectionStore != nil {
		if projection, found, err := a.projectionStore.GetWorktreeByPath(ctx, projectID, worktree); err == nil && found && len(projection.GitStatusRaw) > 0 {
			var cached git.GitStatus
			if unmarshalErr := json.Unmarshal(projection.GitStatusRaw, &cached); unmarshalErr == nil {
				a.refreshGitStatusAsync(projectID, worktree)
				a.ensureStatusPoller(projectID, worktree)
				return &cached, nil
			}
		}
	}

	status, err := a.client.Status(ctx, worktree)
	if err != nil {
		return nil, err
	}
	a.persistStatusSnapshot(ctx, projectID, worktree, status)
	a.ensureStatusPoller(projectID, worktree)
	return status, nil
}

func (a *gitServiceAdapter) refreshGitStatusWriteThrough(ctx context.Context, projectID, worktree string, publishOnChange, forcePublish bool) {
	status, err := a.client.Status(ctx, worktree)
	if err != nil {
		if a.logger != nil {
			a.logger.Debug("git status refresh after mutation failed", "project_id", projectID, "worktree", worktree, "error", err)
		}
		return
	}
	changed, issueID := a.persistStatusSnapshot(ctx, projectID, worktree, status)
	a.invalidateRuntimeSignalCache(normalizeProjectID(projectID), worktree)
	if (forcePublish || (publishOnChange && changed)) && a.onStatusUpdate != nil && strings.TrimSpace(issueID) != "" {
		a.onStatusUpdate(normalizeProjectID(projectID), issueID, worktree)
	}
}

func (a *gitServiceAdapter) refreshGitStatusAsync(projectID, worktree string) {
	projectID = normalizeProjectID(projectID)
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return
	}
	key := projectID + "|" + worktree
	a.refreshMu.Lock()
	if a.refreshRunning == nil {
		a.refreshRunning = map[string]bool{}
	}
	if a.refreshRunning[key] {
		a.refreshMu.Unlock()
		return
	}
	a.refreshRunning[key] = true
	a.refreshMu.Unlock()

	go func() {
		defer func() {
			a.refreshMu.Lock()
			delete(a.refreshRunning, key)
			a.refreshMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		a.refreshGitStatusWriteThrough(ctx, projectID, worktree, true, false)
	}()
}

func (a *gitServiceAdapter) persistStatusSnapshot(ctx context.Context, projectID, worktree string, status *git.GitStatus) (bool, string) {
	if a.projectionStore == nil || status == nil {
		return false, ""
	}
	projection, found, err := a.projectionStore.GetWorktreeByPath(ctx, projectID, worktree)
	if err != nil || !found || strings.TrimSpace(projection.IssueID) == "" {
		return false, ""
	}
	rawStatus, err := json.Marshal(status)
	if err != nil {
		return false, ""
	}
	changed := string(rawStatus) != string(projection.GitStatusRaw)
	if err := a.projectionStore.UpsertWorktreeGitStatus(ctx, projectID, projection.IssueID, rawStatus, time.Now().UTC()); err != nil && a.logger != nil {
		a.logger.Debug("persist git status projection failed", "project_id", projectID, "issue_id", projection.IssueID, "worktree", worktree, "error", err)
		return false, ""
	}
	return changed, projection.IssueID
}

func (a *gitServiceAdapter) ensureStatusPoller(projectID, worktree string) {
	projectID = normalizeProjectID(projectID)
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return
	}
	interval := a.pollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	key := projectID + "|" + worktree
	a.refreshMu.Lock()
	if a.pollers == nil {
		a.pollers = map[string]context.CancelFunc{}
	}
	if _, exists := a.pollers[key]; exists {
		a.refreshMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.pollers[key] = cancel
	a.refreshMu.Unlock()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pollCtx, pollCancel := context.WithTimeout(context.Background(), 3*time.Second)
				a.refreshGitStatusWriteThrough(pollCtx, projectID, worktree, true, false)
				pollCancel()
			}
		}
	}()
}

func normalizeProjectID(projectID string) string {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "default"
	}
	return projectID
}

func (a *gitServiceAdapter) computeRuntimeSignal(ctx context.Context, issueID, worktree, baseBranch string, compareRemote bool, remote string) (daemonhandlers.GitRuntimeSignalsResult, error) {
	out := daemonhandlers.GitRuntimeSignalsResult{
		IssueID:  issueID,
		Worktree: worktree,
	}
	status, err := a.client.Status(ctx, worktree)
	if err != nil {
		return out, err
	}
	out.HasUncommittedChanges = status.HasChanges

	diffStat, err := a.client.DiffStat(ctx, worktree, baseBranch)
	if err != nil {
		return out, err
	}
	out.GitAdditions, out.GitDeletions = parseDiffStatTotalsDaemon(diffStat)

	if compareRemote {
		if err := a.client.Fetch(ctx, worktree, remote); err != nil {
			return out, fmt.Errorf("fetch remote %s before branch-behind check: %w", remote, err)
		}
		behindRevRange := fmt.Sprintf("%s..%s/%s", baseBranch, remote, baseBranch)
		behind, err := a.client.RevListCount(ctx, worktree, behindRevRange)
		if err != nil {
			return out, err
		}
		aheadRevRange := fmt.Sprintf("%s/%s..HEAD", remote, baseBranch)
		ahead, err := a.client.RevListCount(ctx, worktree, aheadRevRange)
		if err != nil {
			return out, err
		}
		out.GitAheadCount = ahead
		out.GitBehindCount = behind
	}

	return out, nil
}

func runtimeSignalCacheKey(projectID, issueID, worktree, baseBranch string, compareRemote bool, remote string) string {
	return strings.ToLower(strings.TrimSpace(projectID)) + "|" +
		strings.ToLower(strings.TrimSpace(issueID)) + "|" +
		strings.TrimSpace(worktree) + "|" +
		strings.TrimSpace(baseBranch) + "|" +
		strconv.FormatBool(compareRemote) + "|" +
		strings.TrimSpace(remote)
}

func (a *gitServiceAdapter) cachedRuntimeSignal(cacheKey string, now time.Time) (daemonhandlers.GitRuntimeSignalsResult, bool) {
	a.runtimeSignalsMu.Lock()
	defer a.runtimeSignalsMu.Unlock()
	if a.runtimeSignalsCache == nil {
		return daemonhandlers.GitRuntimeSignalsResult{}, false
	}
	entry, ok := a.runtimeSignalsCache[cacheKey]
	if !ok || entry.refreshedAt.IsZero() || now.Sub(entry.refreshedAt) > runtimeSignalProjectionTTL {
		return daemonhandlers.GitRuntimeSignalsResult{}, false
	}
	return entry.signal, true
}

func (a *gitServiceAdapter) cachedRuntimeSignalAnyBase(projectID, issueID, worktree string, now time.Time) (daemonhandlers.GitRuntimeSignalsResult, bool) {
	a.runtimeSignalsMu.Lock()
	defer a.runtimeSignalsMu.Unlock()
	if len(a.runtimeSignalsCache) == 0 {
		return daemonhandlers.GitRuntimeSignalsResult{}, false
	}
	prefix := strings.ToLower(strings.TrimSpace(projectID)) + "|" +
		strings.ToLower(strings.TrimSpace(issueID)) + "|" +
		strings.TrimSpace(worktree) + "|"
	var (
		found bool
		best  runtimeSignalProjection
	)
	for key, entry := range a.runtimeSignalsCache {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if entry.refreshedAt.IsZero() || now.Sub(entry.refreshedAt) > runtimeSignalProjectionTTL {
			continue
		}
		if !found || entry.refreshedAt.After(best.refreshedAt) {
			best = entry
			found = true
		}
	}
	if !found {
		return daemonhandlers.GitRuntimeSignalsResult{}, false
	}
	return best.signal, true
}

func (a *gitServiceAdapter) storeRuntimeSignal(cacheKey string, signal daemonhandlers.GitRuntimeSignalsResult, refreshedAt time.Time) {
	a.runtimeSignalsMu.Lock()
	defer a.runtimeSignalsMu.Unlock()
	if a.runtimeSignalsCache == nil {
		a.runtimeSignalsCache = make(map[string]runtimeSignalProjection)
	}
	a.runtimeSignalsCache[cacheKey] = runtimeSignalProjection{
		signal:      signal,
		refreshedAt: refreshedAt,
	}
}

func (a *gitServiceAdapter) invalidateRuntimeSignalCache(projectID, worktree string) {
	a.runtimeSignalsMu.Lock()
	defer a.runtimeSignalsMu.Unlock()
	if len(a.runtimeSignalsCache) == 0 {
		return
	}
	prefix := strings.ToLower(strings.TrimSpace(projectID)) + "|"
	worktreeNeedle := "|" + strings.TrimSpace(worktree) + "|"
	for key := range a.runtimeSignalsCache {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if strings.Contains(key, worktreeNeedle) {
			delete(a.runtimeSignalsCache, key)
		}
	}
}

func parseDiffStatTotalsDaemon(diffStat string) (int, int) {
	var additions, deletions int
	insertionMatches := daemonDiffStatInsertionsPattern.FindAllStringSubmatch(diffStat, -1)
	for _, insertionMatch := range insertionMatches {
		if len(insertionMatch) != 2 {
			continue
		}
		if parsed, err := strconv.Atoi(insertionMatch[1]); err == nil {
			additions += parsed
		}
	}
	deletionMatches := daemonDiffStatDeletionsPattern.FindAllStringSubmatch(diffStat, -1)
	for _, deletionMatch := range deletionMatches {
		if len(deletionMatch) != 2 {
			continue
		}
		if parsed, err := strconv.Atoi(deletionMatch[1]); err == nil {
			deletions += parsed
		}
	}
	return additions, deletions
}
