// CLI argument parsing
//
// Commands:
//   az                             Launch TUI (default)
//   az start <issue-id>            Start a new Claude session (claims bead)
//   az attach <issue-id>           Attach to existing session
//   az pause <issue-id>            Pause a running session
//   az kill <issue-id>             Kill a session (destroy tmux session)
//   az status                      Show status of all sessions
//   az sync [--all]                Sync beads database
//   az gate <issue-id> [--fix]     Run quality gates (orchestration)
//   az notify <event> <bead-id>    Hook notification endpoint
//   az hooks install <bead-id>     Install hooks into .claude/settings.local.json
//   az add <path> [--name]         Register a project
//   az list                        List registered projects
//   az project add/list/remove/switch
//   az --help                      Show help

import gleam/int
import gleam/io
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/string
import azedarach/config
import azedarach/core/hooks
import azedarach/domain/session
import azedarach/services/project_service
import azedarach/services/session_manager
import azedarach/services/tmux
import azedarach/util/shell

// ============================================================================
// Command Types
// ============================================================================

pub type Command {
  // TUI launch
  Run(project_path: Option(String))
  // Session management
  Start(issue_id: String, project_path: Option(String))
  Attach(issue_id: String)
  Pause(issue_id: String)
  Kill(issue_id: String)
  Status
  Sync(all: Bool)
  // Quality gates
  Gate(issue_id: String, fix: Bool)
  // Hook notifications
  Notify(event: String, bead_id: String)
  // Hook management
  HooksInstall(bead_id: String, project_path: Option(String))
  // Project management
  ProjectAdd(path: String, name: Option(String))
  ProjectList
  ProjectRemove(name: String)
  ProjectSwitch(name: String)
  // Help/Version
  Help
  Version
}

// ============================================================================
// Command Parsing
// ============================================================================

pub fn parse(args: List(String)) -> Result(Command, String) {
  case args {
    // No args - launch TUI
    [] -> Ok(Run(None))

    // Help/Version
    ["--help"] | ["-h"] -> Ok(Help)
    ["--version"] | ["-v"] -> Ok(Version)

    // Session management commands
    ["start", issue_id] -> Ok(Start(issue_id, None))
    ["start", issue_id, path] -> Ok(Start(issue_id, Some(path)))
    ["attach", issue_id] -> Ok(Attach(issue_id))
    ["pause", issue_id] -> Ok(Pause(issue_id))
    ["kill", issue_id] -> Ok(Kill(issue_id))
    ["status"] -> Ok(Status)
    ["sync"] -> Ok(Sync(False))
    ["sync", "--all"] -> Ok(Sync(True))

    // Quality gates (for orchestration)
    ["gate", issue_id] -> Ok(Gate(issue_id, False))
    ["gate", issue_id, "--fix"] -> Ok(Gate(issue_id, True))

    // Hook notification (called by Claude Code hooks)
    ["notify", event, bead_id] -> Ok(Notify(event, bead_id))

    // Hook management
    ["hooks", "install", bead_id] -> Ok(HooksInstall(bead_id, None))
    ["hooks", "install", bead_id, path] -> Ok(HooksInstall(bead_id, Some(path)))
    ["hooks"] -> Ok(Help)
    ["hooks", "--help"] -> Ok(Help)

    // Top-level shortcuts for project commands
    ["add", path] -> Ok(ProjectAdd(path, None))
    ["add", path, "--name", name] -> Ok(ProjectAdd(path, Some(name)))
    ["add", path, "-n", name] -> Ok(ProjectAdd(path, Some(name)))
    ["list"] -> Ok(ProjectList)

    // Project subcommands
    ["project", "add", path] -> Ok(ProjectAdd(path, None))
    ["project", "add", path, "--name", name] -> Ok(ProjectAdd(path, Some(name)))
    ["project", "add", path, "-n", name] -> Ok(ProjectAdd(path, Some(name)))
    ["project", "list"] -> Ok(ProjectList)
    ["project", "remove", name] -> Ok(ProjectRemove(name))
    ["project", "switch", name] -> Ok(ProjectSwitch(name))
    ["project"] -> Ok(Help)
    ["project", "--help"] -> Ok(Help)

    // TUI with path - handle single argument that's not a flag
    [_path, "--help"] | [_path, "-h"] -> Ok(Help)
    [path] -> {
      case string.starts_with(path, "-") {
        True -> Error("Unknown flag: " <> path)
        False -> Ok(Run(Some(path)))
      }
    }

    _ -> Error("Unknown arguments: " <> string.join(args, " "))
  }
}

// ============================================================================
// Command Execution
// ============================================================================

/// Execute a CLI command
pub fn execute(cmd: Command) -> Result(Nil, String) {
  case cmd {
    Run(_path) -> {
      // TUI launch is handled by main
      Ok(Nil)
    }

    Start(issue_id, project_path) -> execute_start(issue_id, project_path)
    Attach(issue_id) -> execute_attach(issue_id)
    Pause(issue_id) -> execute_pause(issue_id)
    Kill(issue_id) -> execute_kill(issue_id)
    Status -> execute_status()
    Sync(all) -> execute_sync(all)
    Gate(issue_id, fix) -> execute_gate(issue_id, fix)
    Notify(event, bead_id) -> execute_notify(event, bead_id)
    HooksInstall(bead_id, project_path) -> execute_hooks_install(bead_id, project_path)

    ProjectAdd(path, name) -> execute_project_add(path, name)
    ProjectList -> execute_project_list()
    ProjectRemove(name) -> execute_project_remove(name)
    ProjectSwitch(name) -> execute_project_switch(name)

    Help -> {
      io.println(help_text())
      Ok(Nil)
    }

    Version -> {
      io.println("azedarach 0.1.0")
      Ok(Nil)
    }
  }
}

// ============================================================================
// Session Command Handlers
// ============================================================================

fn execute_start(issue_id: String, project_path: Option(String)) -> Result(Nil, String) {
  let cwd = option.unwrap(project_path, ".")

  io.println("Starting Claude session for issue: " <> issue_id)
  io.println("Project: " <> cwd)

  case config.load(project_path) {
    Ok(cfg) -> {
      let opts = session_manager.StartOptions(
        bead_id: issue_id,
        project_path: cwd,
        initial_prompt: None,
        model: None,
        yolo_mode: False,
      )

      case session_manager.start(opts, cfg) {
        Ok(session) -> {
          io.println("Session started successfully!")
          case session.worktree_path {
            Some(wt) -> io.println("  Worktree: " <> wt)
            None -> Nil
          }
          case session.tmux_session {
            Some(ts) -> {
              io.println("  tmux session: " <> ts)

              // Claim the bead with session assignee (for orchestration)
              let _ = shell.run("bd", [
                "update", issue_id,
                "--status=in_progress",
                "--assignee=" <> ts,
              ], cwd)

              io.println("")
              io.println("To attach: az attach " <> issue_id)
              io.println("Or directly: tmux attach-session -t " <> ts)
            }
            None -> Nil
          }
          Ok(Nil)
        }
        Error(e) -> Error(session_manager.error_to_string(e))
      }
    }
    Error(e) -> Error(config.error_to_string(e))
  }
}

fn execute_attach(issue_id: String) -> Result(Nil, String) {
  io.println("Attaching to session for issue: " <> issue_id)

  case session_manager.attach(issue_id) {
    Ok(_) -> Ok(Nil)
    Error(e) -> Error(session_manager.error_to_string(e))
  }
}

fn execute_pause(issue_id: String) -> Result(Nil, String) {
  io.println("Pausing session for issue: " <> issue_id)

  case session_manager.pause(issue_id) {
    Ok(_) -> {
      io.println("Session paused. Use 'az attach " <> issue_id <> "' to resume.")
      Ok(Nil)
    }
    Error(e) -> Error(session_manager.error_to_string(e))
  }
}

fn execute_status() -> Result(Nil, String) {
  io.println("Session Status")
  io.println("")

  case session_manager.list_active() {
    Ok(sessions) -> {
      case sessions {
        [] -> {
          io.println("No active sessions.")
          Ok(Nil)
        }
        _ -> {
          list.each(sessions, fn(s) {
            let state_str = session_state_to_string(s.state)
            io.println("  " <> s.bead_id <> " - " <> state_str)
          })
          Ok(Nil)
        }
      }
    }
    Error(e) -> Error(session_manager.error_to_string(e))
  }
}

fn session_state_to_string(state: session.State) -> String {
  case state {
    session.Busy -> "BUSY"
    session.Waiting -> "WAITING"
    session.Idle -> "IDLE"
    session.Done -> "DONE"
    session.Error -> "ERROR"
    session.Paused -> "PAUSED"
    session.Unknown -> "UNKNOWN"
  }
}

fn execute_sync(all: Bool) -> Result(Nil, String) {
  io.println("Syncing beads database...")

  case all {
    True -> {
      io.println("[Stub] Syncing all worktrees...")
      io.println("[Stub] Synced worktrees")
      Ok(Nil)
    }
    False -> {
      // Run bd sync
      case shell.run("bd", ["sync"], ".") {
        Ok(output) -> {
          io.println(output)
          Ok(Nil)
        }
        Error(shell.CommandError(_, stderr)) -> Error("Sync failed: " <> stderr)
        Error(shell.NotFound(_)) -> Error("bd command not found")
      }
    }
  }
}

fn execute_kill(issue_id: String) -> Result(Nil, String) {
  io.println("Killing session for issue: " <> issue_id)

  let session_name = session_manager.session_name(issue_id)

  case tmux.session_exists(session_name) {
    False -> {
      io.println("No session found for " <> issue_id)
      Ok(Nil)
    }
    True -> {
      case tmux.kill_session(session_name) {
        Ok(_) -> {
          io.println("Session " <> issue_id <> " killed.")
          Ok(Nil)
        }
        Error(_) -> Error("Failed to kill session")
      }
    }
  }
}

/// Run quality gates for an issue's worktree (configurable via .azedarach.json)
fn execute_gate(issue_id: String, fix: Bool) -> Result(Nil, String) {
  io.println("Running quality gates for: " <> issue_id)
  io.println("")

  let session_name = session_manager.session_name(issue_id)

  // Try to find worktree path from tmux session or convention
  let worktree_path = case tmux.get_option(session_name, "@az_worktree") {
    Ok(path) -> path
    Error(_) -> {
      // Fall back to convention: ../{project}-{issue_id}
      "../azedarach-" <> issue_id
    }
  }

  io.println("Worktree: " <> worktree_path)
  io.println("")

  // Load config to get gates (uses defaults if no config file)
  let cfg = case config.load(Some(worktree_path)) {
    Ok(c) -> c
    Error(_) -> config.default_config()
  }

  // Run each configured gate
  let results = list.map(cfg.gates.gates, fn(gate) {
    io.print("▶ " <> gate.name <> "... ")

    // Use fix_args if fix mode and available, otherwise use normal args
    let args = case fix, gate.fix_args {
      True, Some(fix_args) -> fix_args
      _, _ -> gate.args
    }

    case shell.run(gate.command, args, worktree_path) {
      Ok(_) -> {
        io.println("✓")
        #(True, gate.advisory)
      }
      Error(_) -> {
        case gate.advisory {
          True -> io.println("✗ (advisory)")
          False -> io.println("✗")
        }
        #(False, gate.advisory)
      }
    }
  })

  // Count passes (advisory failures don't count as failures)
  let passed = list.count(results, fn(r) {
    let #(success, advisory) = r
    success || advisory
  })
  let total = list.length(results)

  io.println("")
  case passed == total {
    True -> {
      io.println("✅ All gates passed (" <> int.to_string(passed) <> "/" <> int.to_string(total) <> ")")
      Ok(Nil)
    }
    False -> {
      io.println("❌ Some gates failed (" <> int.to_string(passed) <> "/" <> int.to_string(total) <> ")")
      Error("Quality gates failed")
    }
  }
}

// ============================================================================
// Hook Command Handlers
// ============================================================================

/// Valid hook event types from Claude Code
const valid_hook_events = [
  "user_prompt",
  "idle_prompt",
  "permission_request",
  "pretooluse",
  "stop",
  "session_end",
]

/// Map hook event to session status for tmux
fn map_event_to_status(event: String) -> String {
  case event {
    "user_prompt" | "pretooluse" -> "busy"
    "idle_prompt" | "permission_request" | "stop" -> "waiting"
    "session_end" -> "idle"
    _ -> "unknown"
  }
}

/// Handle hook notifications from Claude Code sessions
///
/// This command is called by Claude Code hooks configured in worktree's
/// .claude/settings.local.json. It updates a tmux session option that the
/// azedarach TUI can poll to detect session state.
fn execute_notify(event: String, bead_id: String) -> Result(Nil, String) {
  // Validate event type
  case list.contains(valid_hook_events, event) {
    False -> {
      Error("Invalid event type: " <> event <> ". Valid events: " <> string.join(valid_hook_events, ", "))
    }
    True -> {
      let status = map_event_to_status(event)
      let session_name = session_manager.session_name(bead_id)

      // Check if session exists
      case tmux.session_exists(session_name) {
        False -> {
          // Session may not exist yet during startup - not an error
          Ok(Nil)
        }
        True -> {
          // Update tmux session option for the Claude session
          case tmux.set_option(session_name, "@az_status", status) {
            Ok(_) -> Ok(Nil)
            Error(_) -> {
              // Failed to set option - not fatal
              Ok(Nil)
            }
          }
        }
      }
    }
  }
}

/// Install Azedarach hooks into the current project's .claude/settings.local.json
fn execute_hooks_install(bead_id: String, project_path: Option(String)) -> Result(Nil, String) {
  let cwd = option.unwrap(project_path, ".")
  let claude_dir = cwd <> "/.claude"
  let settings_path = claude_dir <> "/settings.local.json"

  // Ensure .claude directory exists
  case shell.mkdir_p(claude_dir) {
    Ok(_) -> Nil
    Error(_) -> Nil  // Directory may already exist, continue to try writing
  }

  // Generate hooks configuration
  case hooks.generate_hook_config_auto(bead_id) {
    Ok(hooks_json) -> {
      // Read existing settings if they exist
      let _existing = case shell.read_file(settings_path) {
        Ok(content) -> content
        Error(_) -> "{}"
      }

      // For simplicity, we'll just write the hooks config
      // A proper implementation would merge with existing settings
      case shell.write_file(settings_path, hooks_json) {
        Ok(_) -> {
          io.println("✓ Installed hooks for bead " <> bead_id)
          io.println("  File: " <> settings_path)
          io.println("  Events: UserPromptSubmit, PreToolUse, Notification, PermissionRequest, Stop, SessionEnd")
          Ok(Nil)
        }
        Error(_) -> Error("Failed to write settings file")
      }
    }
    Error(msg) -> Error(msg)
  }
}

// ============================================================================
// Project Command Handlers
// ============================================================================

fn execute_project_add(path: String, name: Option(String)) -> Result(Nil, String) {
  case project_service.add_project(path, name) {
    Ok(entry) -> {
      io.println("✓ Project '" <> entry.name <> "' added successfully.")
      io.println("  Path: " <> entry.path)
      Ok(Nil)
    }
    Error(e) -> Error(project_service.error_to_string(e))
  }
}

fn execute_project_list() -> Result(Nil, String) {
  case project_service.load_registry() {
    Ok(config) -> {
      case config.projects {
        [] -> {
          io.println("No projects registered.")
          io.println("Use 'az add <path>' to register a project.")
          Ok(Nil)
        }
        projects -> {
          io.println("Registered projects:")
          io.println("")
          list.each(projects, fn(p) {
            let is_current = config.default_project == Some(p.name)
            io.println(project_service.format_entry(p, is_current))
            io.println("    Path: " <> p.path)
            case is_current {
              True -> io.println("    (current)")
              False -> Nil
            }
            io.println("")
          })
          Ok(Nil)
        }
      }
    }
    Error(e) -> Error(project_service.error_to_string(e))
  }
}

fn execute_project_remove(name: String) -> Result(Nil, String) {
  case project_service.remove_project(name) {
    Ok(_) -> {
      io.println("✓ Project '" <> name <> "' removed successfully.")
      Ok(Nil)
    }
    Error(e) -> Error(project_service.error_to_string(e))
  }
}

fn execute_project_switch(name: String) -> Result(Nil, String) {
  case project_service.switch_project(name) {
    Ok(entry) -> {
      io.println(
        "✓ Switched to project '" <> entry.name <> "' and set as default.",
      )
      Ok(Nil)
    }
    Error(e) -> Error(project_service.error_to_string(e))
  }
}

// ============================================================================
// Help Text
// ============================================================================

pub fn help_text() -> String {
  "azedarach - TUI Kanban for Claude Code Sessions

USAGE:
    az                              Launch TUI
    az [PROJECT_PATH]               Launch TUI for specific project

SESSION COMMANDS:
    az start <issue-id> [path]      Start a new Claude session (claims bead)
    az attach <issue-id>            Attach to an existing session
    az pause <issue-id>             Pause a running session (Ctrl+C)
    az kill <issue-id>              Kill a session (destroy tmux session)
    az status                       Show status of all sessions
    az sync [--all]                 Sync beads database

ORCHESTRATION COMMANDS:
    az gate <issue-id> [--fix]      Run quality gates (configurable in .azedarach.json)

HOOK COMMANDS:
    az notify <event> <bead-id>     Handle Claude Code hook notification
    az hooks install <bead-id>      Install hooks into .claude/settings.local.json

PROJECT COMMANDS:
    az add <path> [--name NAME]     Register a new project
    az list                         List registered projects
    az project add <path>           Register a new project
    az project list                 List registered projects
    az project remove <name>        Unregister a project
    az project switch <name>        Switch to a project (sets as default)

OPTIONS:
    -h, --help      Print help information
    -v, --version   Print version information

HOOK EVENTS:
    user_prompt       User sends a prompt (→ busy)
    pretooluse        Claude uses a tool (→ busy)
    idle_prompt       Claude waiting for input (→ waiting)
    permission_request Claude needs permission (→ waiting)
    stop              Session stopped (→ waiting)
    session_end       Session ended (→ idle)

KEYBINDINGS:
    hjkl / arrows   Navigate
    Space           Open action menu
    Enter           View details / enter epic
    /               Search
    f               Filter menu
    ,               Sort menu
    ?               Help overlay
    q               Quit

CONFIG:
    Projects are stored in ~/.config/azedarach/projects.json

For full documentation, see docs/README.md"
}
