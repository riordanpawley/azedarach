# Testing Strategy

## Guiding Principle

**Iterate fast, catch regressions early.**

We prioritize:
1. Fast feedback loops (sub-second test runs)
2. High confidence in core logic
3. Low maintenance burden
4. Real integration over mocks where practical

---

## Test Pyramid

```
                    ┌───────────────┐
                    │   Manual /    │  ← Rare, exploratory
                    │   E2E Tests   │
                    └───────────────┘
                   ┌─────────────────────┐
                   │  Integration Tests  │  ← Real tmux, real bd
                   │   (with fixtures)   │
                   └─────────────────────┘
              ┌───────────────────────────────┐
              │         Unit Tests            │  ← Fast, isolated
              │  (pure functions, parsers)    │
              └───────────────────────────────┘
```

---

## Layer 1: Unit Tests (Fast, Many)

### What to Test

| Module | What to Test | Example |
|--------|--------------|---------|
| `state_detector` | Pattern matching on output | `detect("Do you want to continue? [y/n]") == Waiting` |
| `domain/*` | Type construction, validation | `Task.from_json(...)` |
| `config` | JSON parsing, defaults | `Config.load(json) == Ok(config)` |
| `theme` | Color lookups | `Theme.get("catppuccin-macchiato", "text")` |
| `ui/model` | Model updates | `update(model, MoveDown) == model with cursor+1` |
| `ui/keys` | Key mapping | `key_to_action("j", Normal) == MoveDown` |
| `services/beads` | JSON parsing | `parse_bead_json(json) == Ok(bead)` |

### How to Run

```bash
gleam test               # Run all unit tests
gleam test -- --only state_detector  # Run specific module
```

### Example

```gleam
// test/state_detector_test.gleam
import gleeunit/should
import azedarach/services/state_detector

pub fn detect_waiting_test() {
  state_detector.detect("Do you want to continue? [y/n]")
  |> should.equal(state_detector.Waiting("Do you want to continue? [y/n]"))
}

pub fn detect_error_test() {
  state_detector.detect("Error: file not found")
  |> should.equal(state_detector.Error("Error: file not found"))
}

pub fn detect_busy_test() {
  state_detector.detect("Running tests...")
  |> should.equal(state_detector.Busy("Running tests..."))
}
```

### Target

- **Coverage:** >80% on `services/*`, `domain/*`, `ui/model`, `ui/keys`
- **Speed:** Full unit suite < 5 seconds

---

## Layer 2: Integration Tests (Real Dependencies)

### What to Test

| Component | Test With | Example |
|-----------|-----------|---------|
| `tmux` module | Real tmux | Create session, capture pane, send keys |
| `beads` module | Real bd CLI | List beads, show bead, create bead |
| `worktree` module | Real git | Create worktree, check status |
| `clipboard` module | Real clipboard | Paste image (on CI, mock or skip) |

### Setup

```gleam
// test/integration/tmux_test.gleam
import gleam/result
import azedarach/services/tmux

pub fn create_and_capture_test() {
  let session = "test-session-" <> random_id()

  // Create session
  tmux.new_session(session, "/tmp")
  |> should.be_ok()

  // Send command
  tmux.send_keys(session, "echo hello", True)
  |> should.be_ok()

  // Wait briefly
  process.sleep(100)

  // Capture and verify
  let output = tmux.capture_pane(session, 10)
  |> should.be_ok()

  output
  |> string.contains("hello")
  |> should.be_true()

  // Cleanup
  tmux.kill_session(session)
}
```

### CI Considerations

```yaml
# .github/workflows/test.yml
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Install Erlang/Gleam
        uses: erlef/setup-beam@v1
        with:
          otp-version: '27.0'
          gleam-version: '1.6'

      - name: Install tmux
        run: sudo apt-get install -y tmux

      - name: Install beads
        run: cargo install beads  # or however bd is installed

      - name: Run tests
        run: gleam test
```

### Target

- **Coverage:** Core workflows (session create, bead operations)
- **Speed:** Full integration suite < 30 seconds

---

## Layer 3: Actor Tests

### What to Test

Test actor message handling in isolation:

```gleam
// test/actors/coordinator_test.gleam
import azedarach/actors/coordinator

pub fn refresh_updates_tasks_test() {
  // Start coordinator with mock beads data
  let coord = coordinator.start_link(MockBeadsClient)

  // Send refresh message
  coordinator.send(coord, Refresh)

  // Wait and check state
  process.sleep(100)
  let state = coordinator.get_state(coord)

  state.tasks
  |> list.length()
  |> should.equal(3)  // Mock returns 3 beads
}
```

---

## Layer 4: Snapshot Tests (UI)

### What to Test

Render output for specific model states:

```gleam
// test/ui/board_test.gleam
import azedarach/ui/view/board
import azedarach/ui/model.{Model}

pub fn empty_board_snapshot_test() {
  let model = Model(tasks: [], ...)

  board.render(model)
  |> should.equal_snapshot("empty_board")
}

pub fn board_with_tasks_snapshot_test() {
  let model = Model(tasks: [task1, task2], ...)

  board.render(model)
  |> should.equal_snapshot("board_with_tasks")
}
```

Snapshots stored in `test/snapshots/` and updated with:

```bash
gleam test -- --update-snapshots
```

---

## What NOT to Test

| Don't Test | Why |
|------------|-----|
| Shore internals | Library responsibility |
| Erlang/OTP primitives | Well-tested |
| Exact UI layouts | Fragile, snapshot tests instead |
| External CLIs (gh, git) | Integration tests cover |

---

## Test Organization

```
test/
├── unit/
│   ├── state_detector_test.gleam
│   ├── config_test.gleam
│   ├── domain/
│   │   ├── task_test.gleam
│   │   └── bead_test.gleam
│   └── ui/
│       ├── model_test.gleam
│       └── keys_test.gleam
│
├── integration/
│   ├── tmux_test.gleam
│   ├── beads_test.gleam
│   ├── worktree_test.gleam
│   └── clipboard_test.gleam
│
├── actors/
│   ├── coordinator_test.gleam
│   └── session_monitor_test.gleam
│
├── snapshots/
│   ├── empty_board.txt
│   ├── board_with_tasks.txt
│   └── ...
│
└── fixtures/
    ├── beads.json
    ├── config.json
    └── ...
```

---

## Development Workflow

```bash
# During development (fast feedback)
gleam test --only unit          # < 5 seconds

# Before commit
gleam test                      # Full suite < 30 seconds

# CI
gleam test                      # Full suite
gleam format --check            # Formatting
gleam check                     # Type checking
```

---

## Test Utilities

### Fixtures

```gleam
// test/fixtures.gleam
pub fn sample_bead() -> Bead {
  Bead(
    id: "az-123",
    title: "Fix login bug",
    status: Open,
    priority: P2,
    ..
  )
}

pub fn sample_config() -> Config {
  Config(
    worktree: WorktreeConfig(
      path_template: "../{project}-{bead-id}",
      init_commands: ["direnv allow"],
      ..
    ),
    ..
  )
}
```

### Random IDs for Isolation

```gleam
pub fn random_id() -> String {
  erlang.unique_integer([positive])
  |> int.to_string()
}

pub fn test_session_name() -> String {
  "test-" <> random_id()
}
```

### Cleanup Helpers

```gleam
pub fn with_temp_session(f: fn(String) -> a) -> a {
  let session = test_session_name()
  tmux.new_session(session, "/tmp")

  let result = f(session)

  tmux.kill_session(session)
  result
}
```

---

## Summary

| Layer | Count | Speed | Confidence |
|-------|-------|-------|------------|
| Unit | Many (50+) | Fast (<5s) | Logic correctness |
| Integration | Medium (20) | Medium (<30s) | Real dependencies work |
| Actor | Few (10) | Fast | Message handling |
| Snapshot | Few (10) | Fast | UI doesn't regress |

**Total target: < 30 seconds for full suite**
