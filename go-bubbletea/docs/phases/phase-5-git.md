# Phase 5: Git Operations

**Goal**: Git workflow support

**Status**: 🔲 Not Started

## Deliverables

### Git Client
- [ ] Git client (status, fetch, merge, diff, branch, push)
- [ ] Difftastic integration for visual diffs

### Merge Operations
- [ ] Update from main (`Space` `u`)
- [ ] Merge to main (`Space` `m`) with conflict detection
- [ ] Abort merge (`Space` `M`)
- [ ] Merge bead into... (`Space` `b`) with merge select mode
- [ ] Merge choice dialog

### Pull Request
- [ ] Create PR (`Space` `P`) via `gh` CLI

### Diff Viewer
- [ ] Show diff with difftastic (`Space` `f`)

### Status
- [ ] Refresh git stats (`r`)
- [ ] Network status detection
- [ ] Offline mode / graceful degradation
- [ ] Connection status in StatusBar

## Acceptance Criteria

- [ ] Merge detects conflicts and shows affected files
- [ ] Conflict resolution starts Claude session automatically
- [ ] PR creation syncs with main first
- [ ] Diff viewer shows side-by-side difftastic output
- [ ] Offline mode disables push/fetch gracefully

## Dependencies

- [Phase 4: Session Management](phase-4-sessions.md) (for conflict resolution)

## Testing Strategy

| Type | Scope |
|------|-------|
| Unit | Git output parsing |
| Integration | Mock git/gh commands |
| Manual | Full merge workflow |

## Key Implementation Notes

### Git Client

```go
type GitClient struct {
    runner CommandRunner
    logger *slog.Logger
}

func (c *GitClient) Status(ctx context.Context, worktree string) (*GitStatus, error) {
    out, err := c.runner.Run(ctx, "git", "-C", worktree, "status", "--porcelain")
    if err != nil {
        return nil, &domain.GitError{Op: "status", Worktree: worktree, Err: err}
    }
    return parseGitStatus(out)
}

func (c *GitClient) Merge(ctx context.Context, worktree, branch string) (*MergeResult, error) {
    out, err := c.runner.Run(ctx, "git", "-C", worktree, "merge", branch)
    if err != nil {
        // Check for conflicts
        if strings.Contains(string(out), "CONFLICT") {
            return &MergeResult{HasConflicts: true, ConflictFiles: parseConflicts(out)}, nil
        }
        return nil, &domain.GitError{Op: "merge", Worktree: worktree, Err: err}
    }
    return &MergeResult{Success: true}, nil
}
```

### Conflict Detection

```go
type MergeResult struct {
    Success       bool
    HasConflicts  bool
    ConflictFiles []string
    Message       string
}

func (m Model) handleMergeResult(result MergeResult) (tea.Model, tea.Cmd) {
    if result.HasConflicts {
        // Show conflict dialog
        m.overlays.Push(ConflictDialog{
            files: result.ConflictFiles,
            onResolveWithClaude: func() tea.Msg {
                return startConflictResolutionMsg{m.currentTask().ID}
            },
            onAbort: func() tea.Msg {
                return abortMergeMsg{}
            },
        })
        return m, nil
    }

    m.toasts = append(m.toasts, Toast{
        Level:   ToastSuccess,
        Message: "Merge successful",
    })
    return m, nil
}
```

### Network Status Detection

```go
func (m *Model) startNetworkMonitor(program *tea.Program) {
    go func() {
        ticker := time.NewTicker(30 * time.Second)
        defer ticker.Stop()

        for range ticker.C {
            online := checkConnectivity()
            program.Send(networkStatusMsg{online: online})
        }
    }()
}

func checkConnectivity() bool {
    client := &http.Client{Timeout: 5 * time.Second}
    resp, err := client.Head("https://api.github.com")
    if err != nil {
        return false
    }
    resp.Body.Close()
    return resp.StatusCode == 200
}
```

### Offline Mode

```go
type Model struct {
    // ...
    isOnline bool
}

func (m Model) canPush() bool {
    return m.isOnline && m.config.Git.PushEnabled
}

func (m Model) canFetch() bool {
    return m.isOnline && m.config.Git.FetchEnabled
}

// In action menu, disable git operations when offline
func (m Model) getAvailableActions() []Action {
    actions := []Action{
        {Key: "s", Label: "Start session", Enabled: true},
    }

    if m.canPush() {
        actions = append(actions, Action{Key: "P", Label: "Create PR", Enabled: true})
    } else {
        actions = append(actions, Action{Key: "P", Label: "Create PR (offline)", Enabled: false})
    }

    return actions
}
```

### Merge Select Mode

```go
// For "merge bead into..." workflow
type MergeSelectMode struct {
    source     *Task
    candidates []Task
    cursor     int
}

func (m MergeSelectMode) View() string {
    var b strings.Builder
    b.WriteString(fmt.Sprintf("Merge %s into:\n\n", m.source.ID))

    for i, t := range m.candidates {
        prefix := "  "
        if i == m.cursor {
            prefix = "> "
        }
        b.WriteString(fmt.Sprintf("%s%s - %s\n", prefix, t.ID, t.Title))
    }

    return b.String()
}
```

## Files to Create

```
internal/services/git/client.go
internal/services/git/diff.go
internal/services/network/status.go
internal/ui/overlay/merge.go
internal/ui/overlay/conflict.go
internal/ui/diff/viewer.go
```

## Definition of Done

- [ ] All deliverables implemented
- [ ] All acceptance criteria pass
- [ ] Unit tests for git output parsing
- [ ] Integration tests with mock commands
- [ ] Manual testing of merge workflow
- [ ] Offline mode tested
- [ ] Code reviewed and merged
