# Session Lifecycle

## State Machine

```
                                    ┌─────────────────────────────────────┐
                                    │              IDLE                   │
                                    │  (no session, no worktree)          │
                                    └─────────────────────────────────────┘
                                                    │
                                                    │ Space+s / Space+S / Space+!
                                                    │ Space+r (dev server)
                                                    ▼
                                    ┌─────────────────────────────────────┐
                                    │          INITIALIZING               │
                                    │  Creating worktree & session        │
                                    └─────────────────────────────────────┘
                                                    │
                                                    │ Init commands complete
                                                    ▼
                        ┌───────────────────────────────────────────────────────┐
                        │                                                       │
                        ▼                                                       ▼
        ┌───────────────────────────────┐               ┌───────────────────────────────┐
        │            BUSY               │               │           WAITING             │
        │  Claude is working            │◄─────────────►│  Claude needs input           │
        │  (shows recent output)        │               │  (shows prompt)               │
        └───────────────────────────────┘               └───────────────────────────────┘
                        │                                               │
                        │ Space+p                                       │
                        ▼                                               │
        ┌───────────────────────────────┐                               │
        │           PAUSED              │                               │
        │  Ctrl+C sent, WIP commit      │                               │
        │                               │                               │
        │  Space+S-r to resume          │                               │
        └───────────────────────────────┘                               │
                        │                                               │
                        │ Done pattern                                  │ Done pattern
                        ▼                                               ▼
                        ┌───────────────────────────────────────────────┐
                        │                    DONE                       │
                        │  Task completed successfully                  │
                        └───────────────────────────────────────────────┘
                                                    │
                                                    │ Error pattern
                                                    ▼
                        ┌───────────────────────────────────────────────┐
                        │                   ERROR                       │
                        │  Something went wrong                         │
                        └───────────────────────────────────────────────┘

From any state except IDLE:
    Space+x → Stop session (kill tmux)
    Space+a → Attach to session
    Space+d → Cleanup (stop + delete worktree + branch)
```

## Session Creation Flow

### Trigger: Space+s (Start Session)

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ 1. CHECK EXISTING SESSION                                                    │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│   tmux has-session -t {bead-id}-az                                          │
│                                                                              │
│   ├─→ EXISTS: Skip to step 6 (attach)                                       │
│   └─→ NOT EXISTS: Continue to step 2                                        │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
                                        │
                                        ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ 2. CREATE WORKTREE (if needed)                                               │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│   Path: ../project-{bead-id}/ (from pathTemplate config)                    │
│                                                                              │
│   git worktree add ../project-{bead-id} -b {bead-id}                        │
│                                                                              │
│   ├─→ EXISTS: Reuse existing worktree                                       │
│   └─→ CREATED: New isolated git environment                                 │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
                                        │
                                        ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ 3. CREATE TMUX SESSION                                                       │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│   tmux new-session -d -s {bead-id}-az -c {worktree-path}                    │
│                                                                              │
│   Session name: {bead-id}-az (suffix convention)                            │
│   Working directory: worktree path                                          │
│   Detached mode (-d): runs in background                                    │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
                                        │
                                        ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ 4. RUN INIT COMMANDS (once per session)                                      │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│   For each command in config.worktree.initCommands:                         │
│                                                                              │
│   ┌─────────────────────────────────────────────────────────────────────┐   │
│   │ 1. tmux send-keys -t {session} "direnv allow" Enter                 │   │
│   │    └─→ Wait for shell prompt (detects PS1/PS2 pattern)              │   │
│   │                                                                     │   │
│   │ 2. tmux send-keys -t {session} "bun install" Enter                  │   │
│   │    └─→ Wait for shell prompt                                        │   │
│   │                                                                     │   │
│   │ 3. tmux send-keys -t {session} "bd sync" Enter                      │   │
│   │    └─→ Wait for shell prompt                                        │   │
│   │                                                                     │   │
│   │ 4. Set marker: tmux set-option -t {session} @az_init_done 1         │   │
│   └─────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
│   WHY SEQUENTIAL (not &&):                                                   │
│   - direnv hooks trigger on prompt (loads env between commands)             │
│   - Each command failure can be detected independently                      │
│   - User can manually intervene if something hangs                          │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
                                        │
                                        ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ 5. CREATE MAIN WINDOW & START CLAUDE                                         │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│   Main window (window 0):                                                    │
│   tmux send-keys -t {session}:0 "claude" Enter                              │
│                                                                              │
│   For Space+S (start+work), add initial prompt:                             │
│   tmux send-keys -t {session}:0 "claude -p '{prompt}'" Enter                │
│                                                                              │
│   For Space+! (yolo), add dangerous flag:                                   │
│   tmux send-keys -t {session}:0 "claude --dangerously-skip-permissions \    │
│       -p '{prompt}'" Enter                                                  │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
                                        │
                                        ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ 5b. CREATE BACKGROUND TASK WINDOWS (parallel)                                │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│   For each task in config.session.backgroundTasks:                          │
│                                                                              │
│   1. Wait for init marker:                                                   │
│      until [ "$(tmux show-option -t {session} -v @az_init_done)" = "1" ]    │
│                                                                              │
│   2. Create window:                                                          │
│      tmux new-window -t {session} -n task-{N}                               │
│                                                                              │
│   3. Run command:                                                            │
│      tmux send-keys -t {session}:task-{N} "{command}; exit" Enter           │
│                                                                              │
│   Note: Window closes on success, stays open on failure                     │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
                                        │
                                        ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ 6. START SESSION MONITOR                                                     │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│   Spawn actor under Sessions Supervisor:                                     │
│                                                                              │
│   SessionMonitor.start(bead_id, session_name)                               │
│                                                                              │
│   Monitor polls every 500ms:                                                 │
│   - Captures tmux pane output                                               │
│   - Runs state detection patterns                                            │
│   - Sends state changes to Coordinator                                       │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Trigger: Space+r (Dev Server)

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ DEV SERVER FLOW                                                              │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│ 1. Check if session exists:                                                  │
│    ├─→ EXISTS: Skip to step 4                                               │
│    └─→ NOT EXISTS: Do steps 2-4 from Start Session (worktree, session, init)│
│                                                                              │
│ 2. Allocate port:                                                            │
│    - Get base port from config (e.g., 3000)                                 │
│    - If port in use, increment until free                                   │
│    - We TRUST this port (no polling for detected port)                      │
│                                                                              │
│ 3. Create dev server window:                                                 │
│    tmux new-window -t {session} -n dev-{server-name}                        │
│                                                                              │
│ 4. Set PORT and run:                                                         │
│    tmux send-keys -t {session}:dev-{name} \                                 │
│        "PORT={port} {command}" Enter                                        │
│                                                                              │
│ 5. Start Server Monitor:                                                     │
│    - Tracks: running | starting | stopped | error                           │
│    - Knows the port (we set it, trust it)                                   │
│                                                                              │
│ Window naming: dev-{server-name} for easy lookup                            │
│ One window per server (not panes)                                           │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

## State Detection Patterns

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ STATE DETECTION (Priority Order)                                             │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│ WAITING (Priority 100) - Claude needs user input                            │
│ ├── /\[y\/n\]/i                                                             │
│ ├── /Do you want to/i                                                       │
│ ├── /Would you like/i                                                       │
│ ├── /Please confirm/i                                                       │
│ ├── /AskUserQuestion/                                                       │
│ └── ... (12+ patterns)                                                      │
│                                                                              │
│ ERROR (Priority 90) - Something went wrong                                   │
│ ├── /Error:/                                                                │
│ ├── /Exception:/                                                            │
│ ├── /Failed:/                                                               │
│ ├── /ENOENT/                                                                │
│ └── ... (7+ patterns)                                                       │
│                                                                              │
│ DONE (Priority 80) - Task completed                                          │
│ ├── /Task completed/i                                                       │
│ ├── /Successfully/i                                                         │
│ ├── /Done\./                                                                │
│ └── ... (5+ patterns)                                                       │
│                                                                              │
│ BUSY (Default) - Claude is working                                           │
│ └── Any output detected that doesn't match above                            │
│                                                                              │
│ IDLE (Initial) - No session or no output                                     │
│ └── Session doesn't exist or no output captured                             │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Session Operations

### Attach (Space+a)

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ ATTACH FLOW                                                                  │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│ 1. Check branch status:                                                      │
│    git rev-list --count main..HEAD  → commits ahead                         │
│    git rev-list --count HEAD..main  → commits behind                        │
│                                                                              │
│ 2. If behind = 0:                                                            │
│    └─→ Direct attach: exec tmux attach -t {session}                         │
│                                                                              │
│ 3. If behind > 0:                                                            │
│    └─→ Show MergeChoice overlay                                             │
│        ┌─────────────────────────────────────────────────┐                  │
│        │ ↓ Branch Behind main                            │                  │
│        │                                                 │                  │
│        │ 5 commits behind                                │                  │
│        │ Merge main into your branch before attaching?   │                  │
│        │                                                 │                  │
│        │ m: Merge & Attach (pull latest main)           │                  │
│        │ s: Skip & Attach (attach without merge)        │                  │
│        │ Esc: Cancel                                     │                  │
│        └─────────────────────────────────────────────────┘                  │
│                                                                              │
│ 4a. If 'm' (merge):                                                          │
│     └─→ See Merge Flow below                                                │
│                                                                              │
│ 4b. If 's' (skip):                                                           │
│     └─→ Direct attach                                                       │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Merge Flow (from Attach or Space+u)

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ MERGE FLOW                                                                   │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│ 1. Check for conflicts (safe, in-memory):                                    │
│    git merge-tree --write-tree main HEAD                                    │
│    ├─→ Exit 0: No conflicts                                                 │
│    └─→ Exit !0: Conflicts detected                                          │
│                                                                              │
│ 2a. NO CONFLICTS:                                                            │
│     git merge main --no-edit                                                │
│     Toast: "Merged!"                                                        │
│     Continue with attach                                                    │
│                                                                              │
│ 2b. CONFLICTS DETECTED:                                                      │
│     ┌─────────────────────────────────────────────────────────────────────┐ │
│     │ 1. Start merge (creates conflict markers):                          │ │
│     │    git merge main -m "Merge main into {branch}"                     │ │
│     │                                                                     │ │
│     │ 2. Spawn Claude in "merge" window with resolve prompt:              │ │
│     │    "There are merge conflicts in: {files}.                          │ │
│     │     Please resolve these conflicts, then stage and commit."         │ │
│     │                                                                     │ │
│     │ 3. Toast: "Conflicts detected. Claude started to resolve."          │ │
│     │                                                                     │ │
│     │ 4. User can retry attach after Claude resolves                      │ │
│     └─────────────────────────────────────────────────────────────────────┘ │
│                                                                              │
│ Note: .beads/ conflicts are excluded (handled by bd sync)                   │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Pause (Space+p)

```
1. Send Ctrl+C to session:
   tmux send-keys -t {session} C-c

2. Create WIP commit:
   git add -A && git commit -m "wip: paused session"

3. Update state: PAUSED
```

### Resume (Space+Shift+r)

```
1. Resume Claude:
   tmux send-keys -t {session} "claude" Enter

2. Update state: BUSY → monitors take over
```

### Stop (Space+x)

```
1. Kill tmux session:
   tmux kill-session -t {session}

2. Stop session monitor (automatic via supervisor)

3. Update state: IDLE
```

### Cleanup/Delete (Space+d)

```
1. Stop session (if running):
   tmux kill-session -t {session}

2. Delete worktree:
   git worktree remove {path} --force

3. Delete remote branch:
   git push origin --delete {branch}

4. Delete local branch:
   git branch -D {branch}

5. Optionally close bead:
   bd close {bead-id}

6. Update state: removed from model
```

## Window Structure

```
Session: az-123-az
│
├── Window 0: main
│   └── Claude Code (or shell if no Claude)
│
├── Window: dev-web
│   └── npm run dev (PORT=3000)
│
├── Window: dev-api
│   └── npm run api (PORT=8000)
│
├── Window: task-1
│   └── npm run watch
│   └── (closes on success, stays on failure)
│
├── Window: task-2
│   └── npm run test:watch
│   └── (closes on success, stays on failure)
│
└── Window: merge (if conflicts)
    └── Claude resolving conflicts
```
