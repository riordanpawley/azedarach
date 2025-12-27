// Coordinator Actor - central orchestration
// Manages task cache, session registry, routes commands
// Handles project switching and periodic refresh
// Integrates with OTP supervision tree for session/server monitors

import gleam/dict.{type Dict}
import gleam/erlang/process.{type Subject}
import gleam/int
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/otp/actor
import gleam/result
import gleam/string
// Erlang FFI for unique integer generation
@external(erlang, "erlang", "unique_integer")
fn unique_integer() -> Int
import azedarach/config.{type Config, Local, Origin}
import azedarach/domain/task.{type Task}
import azedarach/domain/session.{type SessionState}
import azedarach/domain/project.{type Project}
import azedarach/services/beads
import azedarach/services/bead_editor
import azedarach/services/dev_server_state.{type DevServerState, DevServerState}
import azedarach/services/image
import azedarach/services/port_allocator
import azedarach/services/tmux
import azedarach/services/worktree
import azedarach/services/git
import azedarach/services/project_service
import tempo

// Refresh interval in milliseconds (2 seconds)
const refresh_interval_ms = 2000

// Actor state
pub type CoordinatorState {
  CoordinatorState(
    config: Config,
    tasks: List(Task),
    sessions: Dict(String, SessionState),
    dev_servers: Dict(String, DevServerState),
    port_allocator: Option(Subject(port_allocator.Msg)),
    ui_subject: Option(Subject(UiMsg)),
    // Project management
    current_project: Option(Project),
    available_projects: List(Project),
    // Self reference for periodic refresh
    self_subject: Option(Subject(Msg)),
  )
}

// DevServerState is imported from dev_server_state module

// Messages the coordinator can receive
pub type Msg {
  // Subscription
  Subscribe(Subject(UiMsg))
  // Lifecycle
  Initialize
  PeriodicTick
  // Project management
  SwitchProject(path: String)
  RefreshProjects
  InitBeadsInProject
  // Beads
  RefreshBeads
  CreateBead(title: Option(String), issue_type: task.IssueType)
  CreateBeadViaEditor(issue_type: task.IssueType)
  EditBead(id: String)
  DeleteBead(id: String)
  MoveTask(id: String, direction: Int)
  SearchBeads(pattern: String)
  GetReadyBeads
  // Sessions
  StartSession(id: String, with_work: Bool, yolo: Bool)
  AttachSession(id: String)
  PauseSession(id: String)
  ResumeSession(id: String)
  StopSession(id: String)
  MergeAndAttach(id: String)
  AbortMerge(id: String)
  // Dev servers
  ToggleDevServer(id: String, server: String)
  ViewDevServer(id: String, server: String)
  RestartDevServer(id: String, server: String)
  // Git
  UpdateFromMain(id: String)
  MergeToMain(id: String)
  CreatePR(id: String)
  CompleteSession(id: String)
  DeleteCleanup(id: String)
  // Images
  PasteImage(id: String)
  DeleteImage(id: String, attachment_id: String)
  // Dependencies
  AddDependency(id: String, depends_on: String, dep_type: task.DependentType)
  RemoveDependency(id: String, depends_on: String)
  // Internal
  BeadsLoaded(Result(List(Task), beads.BeadsError))
  ProjectsDiscovered(List(Project))
  InitialProjectSelected(Result(Project, project_service.ProjectError))
  SessionMonitorUpdate(id: String, state: session.State)
}

// Messages sent to UI
pub type UiMsg {
  TasksUpdated(List(Task))
  SearchResults(List(Task))
  SessionStateChanged(String, SessionState)
  DevServerStateChanged(String, DevServerState)
  Toast(message: String, level: ToastLevel)
  RequestMergeChoice(bead_id: String, behind_count: Int)
  // Project updates
  ProjectChanged(Project)
  ProjectsUpdated(List(Project))
}

pub type ToastLevel {
  Info
  Success
  Warning
  ErrorLevel
}

// ============================================================================
// Helper Functions
// ============================================================================

/// Get project path from state, falling back to "." if no project is selected
fn get_project_path(state: CoordinatorState) -> String {
  case state.current_project {
    Some(proj) -> proj.path
    None -> "."
  }
}

// Start the coordinator
pub fn start(config: Config) -> Result(Subject(Msg), actor.StartError) {
  // Start port allocator
  let allocator = case port_allocator.start() {
    Ok(a) -> Some(a)
    Error(_) -> None
  }

  let initial_state =
    CoordinatorState(
      config: config,
      tasks: [],
      sessions: dict.new(),
      dev_servers: dict.new(),
      port_allocator: allocator,
      ui_subject: None,
      current_project: None,
      available_projects: [],
      self_subject: None,
    )

  actor.new(initial_state)
  |> actor.on_message(handle_message)
  |> actor.start
  |> result.map(fn(started) { started.data })
}

/// Initialize the coordinator after creation
/// Call this after start() to begin periodic refresh and project discovery
pub fn initialize(subject: Subject(Msg)) -> Nil {
  process.send(subject, Initialize)
}

// Send message to coordinator
pub fn send(subject: Subject(Msg), msg: Msg) -> Nil {
  process.send(subject, msg)
}

// Message handler
fn handle_message(
  state: CoordinatorState,
  msg: Msg,
) -> actor.Next(CoordinatorState, Msg) {
  case msg {
    Subscribe(ui) -> {
      actor.continue(CoordinatorState(..state, ui_subject: Some(ui)))
    }

    Initialize -> {
      // Store self reference for periodic refresh
      let self = process.new_subject()

      // Start project discovery
      spawn_project_discovery(self)
      spawn_initial_project(self)

      // Schedule first periodic tick
      schedule_tick(self)

      actor.continue(CoordinatorState(..state, self_subject: Some(self)))
    }

    PeriodicTick -> {
      // Refresh beads for current project
      case state.current_project {
        Some(proj) -> spawn_beads_load_for_project(state, proj)
        None -> Nil
      }

      // Schedule next tick
      case state.self_subject {
        Some(self) -> schedule_tick(self)
        None -> Nil
      }

      actor.continue(state)
    }

    ProjectsDiscovered(projects) -> {
      notify_ui(state, ProjectsUpdated(projects))
      actor.continue(CoordinatorState(..state, available_projects: projects))
    }

    InitialProjectSelected(result) -> {
      case result {
        Ok(proj) -> {
          notify_ui(state, ProjectChanged(proj))
          notify_ui(state, Toast("Project: " <> project.display_name(proj), Info))
          // Load beads for this project
          spawn_beads_load_for_project(state, proj)
          actor.continue(CoordinatorState(..state, current_project: Some(proj)))
        }
        Error(e) -> {
          notify_ui(state, Toast(project_service.error_to_string(e), Warning))
          actor.continue(state)
        }
      }
    }

    SwitchProject(path) -> {
      case project_service.switch_to(path) {
        Ok(proj) -> {
          notify_ui(state, ProjectChanged(proj))
          notify_ui(state, Toast("Switched to " <> project.display_name(proj), Success))
          // Clear tasks and reload for new project
          spawn_beads_load_for_project(state, proj)
          actor.continue(CoordinatorState(
            ..state,
            current_project: Some(proj),
            tasks: [],
          ))
        }
        Error(e) -> {
          notify_ui(state, Toast(project_service.error_to_string(e), ErrorLevel))
          actor.continue(state)
        }
      }
    }

    RefreshProjects -> {
      case state.self_subject {
        Some(self) -> spawn_project_discovery(self)
        None -> Nil
      }
      actor.continue(state)
    }

    InitBeadsInProject -> {
      case state.current_project {
        Some(proj) -> {
          case project_service.init_beads(proj) {
            Ok(_) -> {
              notify_ui(state, Toast("Beads initialized", Success))
              spawn_beads_load_for_project(state, proj)
            }
            Error(e) -> {
              notify_ui(state, Toast(project_service.error_to_string(e), ErrorLevel))
            }
          }
        }
        None -> notify_ui(state, Toast("No project selected", Warning))
      }
      actor.continue(state)
    }

    RefreshBeads -> {
      // Async load beads
      let self = process.new_subject()
      spawn_beads_load(self, get_project_path(state), state.config)
      actor.continue(state)
    }

    BeadsLoaded(result) -> {
      case result {
        Ok(tasks) -> {
          notify_ui(state, TasksUpdated(tasks))
          actor.continue(CoordinatorState(..state, tasks: tasks))
        }
        Error(e) -> {
          notify_ui(state, Toast(beads.error_to_string(e), ErrorLevel))
          actor.continue(state)
        }
      }
    }

    CreateBead(title, issue_type) -> {
      let options = case title {
        Some(t) ->
          beads.CreateOptions(
            ..beads.default_create_options(),
            title: Some(t),
            issue_type: Some(issue_type),
          )
        None ->
          beads.CreateOptions(
            ..beads.default_create_options(),
            issue_type: Some(issue_type),
          )
      }
      let project_path = get_project_path(state)
      case beads.create(options, project_path, state.config) {
        Ok(id) -> {
          notify_ui(state, Toast("Bead created: " <> id, Success))
          spawn_beads_load(process.new_subject(), get_project_path(state), state.config)
        }
        Error(e) -> notify_ui(state, Toast(beads.error_to_string(e), ErrorLevel))
      }
      actor.continue(state)
    }

    CreateBeadViaEditor(issue_type) -> {
      let project_path = get_project_path(state)
      case bead_editor.create_bead(issue_type, state.config) {
        Ok(parsed) -> {
          case bead_editor.create_from_parsed(parsed, issue_type, project_path, state.config) {
            Ok(id) -> {
              notify_ui(state, Toast("Bead created: " <> id, Success))
              spawn_beads_load(process.new_subject(), project_path, state.config)
            }
            Error(e) ->
              notify_ui(state, Toast(bead_editor.error_to_string(e), ErrorLevel))
          }
        }
        Error(e) -> notify_ui(state, Toast(bead_editor.error_to_string(e), ErrorLevel))
      }
      actor.continue(state)
    }

    EditBead(id) -> {
      let project_path = get_project_path(state)
      case bead_editor.edit_bead(id, project_path, state.config) {
        Ok(parsed) -> {
          case bead_editor.apply_changes(id, parsed, project_path, state.config) {
            Ok(_) -> {
              notify_ui(state, Toast("Bead updated", Success))
              spawn_beads_load(process.new_subject(), project_path, state.config)
            }
            Error(e) ->
              notify_ui(state, Toast(bead_editor.error_to_string(e), ErrorLevel))
          }
        }
        Error(e) -> notify_ui(state, Toast(bead_editor.error_to_string(e), ErrorLevel))
      }
      actor.continue(state)
    }

    DeleteBead(id) -> {
      let project_path = get_project_path(state)
      case beads.delete(id, project_path, state.config) {
        Ok(_) -> {
          notify_ui(state, Toast("Bead deleted", Success))
          spawn_beads_load(process.new_subject(), get_project_path(state), state.config)
        }
        Error(e) -> notify_ui(state, Toast(beads.error_to_string(e), ErrorLevel))
      }
      actor.continue(state)
    }

    MoveTask(id, direction) -> {
      let project_path = get_project_path(state)
      case list.find(state.tasks, fn(t) { t.id == id }) {
        Ok(found_task) -> {
          let new_status = next_status(found_task.status, direction)
          case beads.update_status(id, new_status, project_path, state.config) {
            Ok(_) -> spawn_beads_load(process.new_subject(), get_project_path(state), state.config)
            Error(e) -> notify_ui(state, Toast(beads.error_to_string(e), ErrorLevel))
          }
        }
        Error(_) -> Nil
      }
      actor.continue(state)
    }

    SearchBeads(pattern) -> {
      let project_path = get_project_path(state)
      case beads.search(pattern, project_path, state.config) {
        Ok(results) -> notify_ui(state, SearchResults(results))
        Error(e) -> notify_ui(state, Toast(beads.error_to_string(e), ErrorLevel))
      }
      actor.continue(state)
    }

    GetReadyBeads -> {
      let project_path = get_project_path(state)
      case beads.ready(project_path, state.config) {
        Ok(results) -> notify_ui(state, SearchResults(results))
        Error(e) -> notify_ui(state, Toast(beads.error_to_string(e), ErrorLevel))
      }
      actor.continue(state)
    }

    StartSession(id, with_work, yolo) -> {
      let new_state = handle_start_session(state, id, with_work, yolo)
      actor.continue(new_state)
    }

    AttachSession(id) -> {
      case dict.get(state.sessions, id) {
        Ok(session_state) -> {
          case session_state.worktree_path {
            Some(path) -> {
              case git.commits_behind_main(path, state.config) {
                Ok(behind) if behind > 0 -> {
                  notify_ui(state, RequestMergeChoice(id, behind))
                }
                _ -> {
                  let tmux_name =
                    session_state.tmux_session |> option.unwrap(id <> "-az")
                  case tmux.attach(tmux_name) {
                    Ok(_) -> Nil
                    Error(e) -> notify_ui(state, Toast("Failed to attach: " <> tmux.error_to_string(e), ErrorLevel))
                  }
                }
              }
            }
            None -> notify_ui(state, Toast("No worktree for session", Warning))
          }
        }
        Error(_) -> notify_ui(state, Toast("Session not found", Warning))
      }
      actor.continue(state)
    }

    PauseSession(id) -> {
      case dict.get(state.sessions, id) {
        Ok(session_state) -> {
          case session_state.tmux_session {
            Some(tmux_name) -> {
              case tmux.send_keys(tmux_name, "C-c") {
                Ok(_) -> {
                  case session_state.worktree_path {
                    Some(path) -> {
                      // WIP commit is best-effort - don't fail pause if it fails
                      case git.wip_commit(path) {
                        Ok(_) -> Nil
                        Error(_) -> Nil
                      }
                    }
                    None -> Nil
                  }
                  let new_session =
                    session.SessionState(..session_state, state: session.Paused)
                  let new_sessions = dict.insert(state.sessions, id, new_session)
                  notify_ui(state, SessionStateChanged(id, new_session))
                  actor.continue(
                    CoordinatorState(..state, sessions: new_sessions),
                  )
                }
                Error(e) -> {
                  notify_ui(state, Toast("Failed to pause: " <> tmux.error_to_string(e), ErrorLevel))
                  actor.continue(state)
                }
              }
            }
            None -> {
              notify_ui(state, Toast("No tmux session", Warning))
              actor.continue(state)
            }
          }
        }
        Error(_) -> {
          notify_ui(state, Toast("Session not found", Warning))
          actor.continue(state)
        }
      }
    }

    ResumeSession(id) -> {
      case dict.get(state.sessions, id) {
        Ok(session_state) -> {
          case session_state.tmux_session {
            Some(tmux_name) -> {
              case tmux.send_keys(tmux_name, "claude") {
                Ok(_) -> {
                  case tmux.send_keys(tmux_name, "Enter") {
                    Ok(_) -> {
                      let new_session =
                        session.SessionState(..session_state, state: session.Busy)
                      let new_sessions = dict.insert(state.sessions, id, new_session)
                      notify_ui(state, SessionStateChanged(id, new_session))
                      actor.continue(
                        CoordinatorState(..state, sessions: new_sessions),
                      )
                    }
                    Error(e) -> {
                      notify_ui(state, Toast("Failed to send Enter: " <> tmux.error_to_string(e), ErrorLevel))
                      actor.continue(state)
                    }
                  }
                }
                Error(e) -> {
                  notify_ui(state, Toast("Failed to resume: " <> tmux.error_to_string(e), ErrorLevel))
                  actor.continue(state)
                }
              }
            }
            None -> {
              notify_ui(state, Toast("No tmux session", Warning))
              actor.continue(state)
            }
          }
        }
        Error(_) -> {
          notify_ui(state, Toast("Session not found", Warning))
          actor.continue(state)
        }
      }
    }

    StopSession(id) -> {
      case dict.get(state.sessions, id) {
        Ok(session_state) -> {
          case session_state.tmux_session {
            Some(tmux_name) -> {
              case tmux.kill_session(tmux_name) {
                Ok(_) -> {
                  let new_sessions = dict.delete(state.sessions, id)
                  notify_ui(
                    state,
                    SessionStateChanged(
                      id,
                      session.SessionState(
                        bead_id: id,
                        state: session.Idle,
                        started_at: None,
                        last_output: None,
                        worktree_path: session_state.worktree_path,
                        tmux_session: None,
                      ),
                    ),
                  )
                  actor.continue(CoordinatorState(..state, sessions: new_sessions))
                }
                Error(e) -> {
                  notify_ui(state, Toast("Failed to stop: " <> tmux.error_to_string(e), ErrorLevel))
                  actor.continue(state)
                }
              }
            }
            None -> actor.continue(state)
          }
        }
        Error(_) -> actor.continue(state)
      }
    }

    MergeAndAttach(id) -> {
      case dict.get(state.sessions, id) {
        Ok(session_state) -> {
          case session_state.worktree_path {
            Some(path) -> {
              case git.merge_main(path, state.config) {
                Ok(_) -> {
                  notify_ui(state, Toast("Merged main", Success))
                  let tmux_name =
                    session_state.tmux_session |> option.unwrap(id <> "-az")
                  case tmux.attach(tmux_name) {
                    Ok(_) -> Nil
                    Error(e) -> notify_ui(state, Toast("Failed to attach after merge: " <> tmux.error_to_string(e), ErrorLevel))
                  }
                }
                Error(git.MergeConflict(files)) -> {
                  let tmux_name =
                    session_state.tmux_session |> option.unwrap(id <> "-az")
                  case tmux.new_window(tmux_name, "merge") {
                    Ok(_) -> {
                      let prompt =
                        "There are merge conflicts in: "
                        <> string.join(files, ", ")
                        <> ". Please resolve these conflicts, then stage and commit the resolution."
                      case tmux.send_keys(tmux_name <> ":merge", "claude -p \"" <> prompt <> "\"") {
                        Ok(_) -> {
                          case tmux.send_keys(tmux_name <> ":merge", "Enter") {
                            Ok(_) -> Nil
                            Error(e) -> notify_ui(state, Toast("Failed to start resolver: " <> tmux.error_to_string(e), ErrorLevel))
                          }
                        }
                        Error(e) -> notify_ui(state, Toast("Failed to start resolver: " <> tmux.error_to_string(e), ErrorLevel))
                      }
                    }
                    Error(e) -> notify_ui(state, Toast("Failed to create merge window: " <> tmux.error_to_string(e), ErrorLevel))
                  }
                  notify_ui(
                    state,
                    Toast(
                      "Conflicts detected. Claude started in 'merge' window.",
                      Warning,
                    ),
                  )
                }
                Error(e) -> notify_ui(state, Toast(git.error_to_string(e), ErrorLevel))
              }
            }
            None -> notify_ui(state, Toast("No worktree", Warning))
          }
        }
        Error(_) -> notify_ui(state, Toast("Session not found", Warning))
      }
      actor.continue(state)
    }

    AbortMerge(id) -> {
      case dict.get(state.sessions, id) {
        Ok(session_state) -> {
          case session_state.worktree_path {
            Some(path) -> {
              case git.abort_merge(path) {
                Ok(_) -> notify_ui(state, Toast("Merge aborted", Success))
                Error(e) -> notify_ui(state, Toast(git.error_to_string(e), ErrorLevel))
              }
            }
            None -> notify_ui(state, Toast("No worktree", Warning))
          }
        }
        Error(_) -> notify_ui(state, Toast("Session not found", Warning))
      }
      actor.continue(state)
    }

    ToggleDevServer(id, server_name) -> {
      let key = id <> ":" <> server_name
      case dict.get(state.dev_servers, key) {
        Ok(ds) -> {
          case dev_server_state.is_running(ds) {
            True -> {
              let window = ds.window_name
              case tmux.kill_window(id <> "-az", window) {
                Ok(_) -> Nil
                Error(e) -> notify_ui(state, Toast("Failed to stop dev server window: " <> tmux.error_to_string(e), Warning))
              }
              // Release port from allocator
              case state.port_allocator, ds.port {
                Some(allocator), Some(_) ->
                  port_allocator.release(allocator, key)
                _, _ -> Nil
              }
              let new_ds = dev_server_state.make_idle(server_name)
              let new_servers = dict.insert(state.dev_servers, key, new_ds)
              notify_ui(state, DevServerStateChanged(key, new_ds))
              actor.continue(CoordinatorState(..state, dev_servers: new_servers))
            }
            False -> {
              let new_state = ensure_session(state, id)
              start_dev_server(new_state, id, server_name)
            }
          }
        }
        Error(_) -> {
          let new_state = ensure_session(state, id)
          start_dev_server(new_state, id, server_name)
        }
      }
    }

    ViewDevServer(id, server_name) -> {
      let key = id <> ":" <> server_name
      case dict.get(state.dev_servers, key) {
        Ok(ds) -> {
          case tmux.select_window(id <> "-az", ds.window_name) {
            Ok(_) -> Nil
            Error(e) -> notify_ui(state, Toast("Failed to select window: " <> tmux.error_to_string(e), ErrorLevel))
          }
        }
        Error(_) -> notify_ui(state, Toast("Dev server not running", Warning))
      }
      actor.continue(state)
    }

    RestartDevServer(id, server_name) -> {
      let key = id <> ":" <> server_name
      case dict.get(state.dev_servers, key) {
        Ok(ds) -> {
          case tmux.send_keys(id <> "-az:" <> ds.window_name, "C-c") {
            Ok(_) -> Nil
            Error(e) -> notify_ui(state, Toast("Failed to stop dev server: " <> tmux.error_to_string(e), Warning))
          }
          process.sleep(100)
          case
            list.find(state.config.dev_server.servers, fn(s) {
              s.name == server_name
            })
          {
            Ok(server) -> {
              case tmux.send_keys(id <> "-az:" <> ds.window_name, server.command) {
                Ok(_) -> {
                  case tmux.send_keys(id <> "-az:" <> ds.window_name, "Enter") {
                    Ok(_) -> Nil
                    Error(e) -> notify_ui(state, Toast("Failed to restart dev server: " <> tmux.error_to_string(e), ErrorLevel))
                  }
                }
                Error(e) -> notify_ui(state, Toast("Failed to restart dev server: " <> tmux.error_to_string(e), ErrorLevel))
              }
            }
            Error(_) -> Nil
          }
        }
        Error(_) -> notify_ui(state, Toast("Dev server not running", Warning))
      }
      actor.continue(state)
    }

    UpdateFromMain(id) -> {
      case dict.get(state.sessions, id) {
        Ok(session_state) -> {
          case session_state.worktree_path {
            Some(path) -> {
              case git.merge_main(path, state.config) {
                Ok(_) -> notify_ui(state, Toast("Updated from main", Success))
                Error(git.MergeConflict(files)) -> {
                  let tmux_name = id <> "-az"
                  case tmux.new_window(tmux_name, "merge") {
                    Ok(_) -> {
                      let prompt =
                        "There are merge conflicts. Please resolve: "
                        <> string.join(files, ", ")
                      case tmux.send_keys(tmux_name <> ":merge", "claude -p \"" <> prompt <> "\"") {
                        Ok(_) -> {
                          case tmux.send_keys(tmux_name <> ":merge", "Enter") {
                            Ok(_) -> notify_ui(state, Toast("Conflicts detected. Claude resolving.", Warning))
                            Error(e) -> notify_ui(state, Toast("Failed to start resolver: " <> tmux.error_to_string(e), ErrorLevel))
                          }
                        }
                        Error(e) -> notify_ui(state, Toast("Failed to start resolver: " <> tmux.error_to_string(e), ErrorLevel))
                      }
                    }
                    Error(e) -> notify_ui(state, Toast("Failed to create merge window: " <> tmux.error_to_string(e), ErrorLevel))
                  }
                }
                Error(e) -> notify_ui(state, Toast(git.error_to_string(e), ErrorLevel))
              }
            }
            None -> notify_ui(state, Toast("No worktree", Warning))
          }
        }
        Error(_) -> notify_ui(state, Toast("Session not found", Warning))
      }
      actor.continue(state)
    }

    MergeToMain(id) -> {
      case dict.get(state.sessions, id) {
        Ok(session_state) -> {
          case session_state.worktree_path {
            Some(path) -> {
              case git.merge_to_main(path, state.config) {
                Ok(_) -> notify_ui(state, Toast("Merged to main", Success))
                Error(e) -> notify_ui(state, Toast(git.error_to_string(e), ErrorLevel))
              }
            }
            None -> notify_ui(state, Toast("No worktree", Warning))
          }
        }
        Error(_) -> notify_ui(state, Toast("No session", Warning))
      }
      actor.continue(state)
    }

    CreatePR(id) -> {
      case dict.get(state.sessions, id) {
        Ok(session_state) -> {
          case session_state.worktree_path {
            Some(path) -> {
              case git.create_pr(path, id, state.config) {
                Ok(url) -> notify_ui(state, Toast("PR created: " <> url, Success))
                Error(e) -> notify_ui(state, Toast(git.error_to_string(e), ErrorLevel))
              }
            }
            None -> notify_ui(state, Toast("No worktree", Warning))
          }
        }
        Error(_) -> notify_ui(state, Toast("No session", Warning))
      }
      actor.continue(state)
    }

    // Complete session based on workflow mode:
    // - Local mode: merge directly to base branch
    // - Origin mode: create PR
    CompleteSession(id) -> {
      case dict.get(state.sessions, id) {
        Ok(session_state) -> {
          case session_state.worktree_path {
            Some(path) -> {
              case state.config.git.workflow_mode {
                Local -> {
                  // Local mode: merge directly to main
                  case git.merge_to_main(path, state.config) {
                    Ok(_) -> notify_ui(state, Toast("Merged to " <> state.config.git.base_branch, Success))
                    Error(e) -> notify_ui(state, Toast(git.error_to_string(e), ErrorLevel))
                  }
                }
                Origin -> {
                  // Origin mode: create PR
                  case git.create_pr(path, id, state.config) {
                    Ok(url) -> notify_ui(state, Toast("PR created: " <> url, Success))
                    Error(e) -> notify_ui(state, Toast(git.error_to_string(e), ErrorLevel))
                  }
                }
              }
            }
            None -> notify_ui(state, Toast("No worktree", Warning))
          }
        }
        Error(_) -> notify_ui(state, Toast("No session", Warning))
      }
      actor.continue(state)
    }

    DeleteCleanup(id) -> {
      let project_path = get_project_path(state)
      case dict.get(state.sessions, id) {
        Ok(session_state) -> {
          case session_state.tmux_session {
            Some(tmux_name) -> {
              case tmux.kill_session(tmux_name) {
                Ok(_) -> Nil
                Error(e) -> notify_ui(state, Toast("Failed to kill session: " <> tmux.error_to_string(e), Warning))
              }
            }
            None -> Nil
          }
          case session_state.worktree_path {
            Some(path) -> {
              case worktree.delete(path, project_path) {
                Ok(_) -> Nil
                Error(e) -> notify_ui(state, Toast("Failed to delete worktree: " <> worktree.error_to_string(e), Warning))
              }
              case git.delete_branch(id, state.config, project_path) {
                Ok(_) -> Nil
                Error(e) -> notify_ui(state, Toast("Failed to delete branch: " <> git.error_to_string(e), Warning))
              }
            }
            None -> Nil
          }
          let new_sessions = dict.delete(state.sessions, id)
          notify_ui(state, Toast("Cleanup complete", Success))
          actor.continue(CoordinatorState(..state, sessions: new_sessions))
        }
        Error(_) -> {
          notify_ui(state, Toast("Nothing to cleanup", Info))
          actor.continue(state)
        }
      }
    }

    PasteImage(id) -> {
      let project_path = get_project_path(state)
      case image.attach_from_clipboard(id) {
        Ok(attachment) -> {
          let link = image.build_notes_link(id, attachment)
          case beads.append_notes(id, link, project_path, state.config) {
            Ok(_) ->
              notify_ui(
                state,
                Toast("Image attached: " <> attachment.filename, Success),
              )
            Error(e) -> notify_ui(state, Toast(beads.error_to_string(e), ErrorLevel))
          }
        }
        Error(e) -> notify_ui(state, Toast(image.error_to_string(e), ErrorLevel))
      }
      actor.continue(state)
    }

    DeleteImage(id, attachment_id) -> {
      let project_path = get_project_path(state)
      case image.remove(id, attachment_id) {
        Ok(attachment) -> {
          case remove_image_from_notes(id, attachment.filename, project_path, state.config) {
            Ok(_) -> notify_ui(state, Toast("Image deleted", Success))
            Error(e) -> notify_ui(state, Toast(beads.error_to_string(e), ErrorLevel))
          }
        }
        Error(e) -> notify_ui(state, Toast(image.error_to_string(e), ErrorLevel))
      }
      actor.continue(state)
    }

    AddDependency(id, depends_on, dep_type) -> {
      let project_path = get_project_path(state)
      case beads.add_dependency(id, depends_on, dep_type, project_path, state.config) {
        Ok(_) -> {
          notify_ui(state, Toast("Dependency added", Success))
          spawn_beads_load(process.new_subject(), get_project_path(state), state.config)
        }
        Error(e) -> notify_ui(state, Toast(beads.error_to_string(e), ErrorLevel))
      }
      actor.continue(state)
    }

    RemoveDependency(id, depends_on) -> {
      let project_path = get_project_path(state)
      case beads.remove_dependency(id, depends_on, project_path, state.config) {
        Ok(_) -> {
          notify_ui(state, Toast("Dependency removed", Success))
          spawn_beads_load(process.new_subject(), get_project_path(state), state.config)
        }
        Error(e) -> notify_ui(state, Toast(beads.error_to_string(e), ErrorLevel))
      }
      actor.continue(state)
    }

    SessionMonitorUpdate(id, new_state) -> {
      case dict.get(state.sessions, id) {
        Ok(session_state) -> {
          let updated =
            session.SessionState(..session_state, state: new_state)
          let new_sessions = dict.insert(state.sessions, id, updated)
          notify_ui(state, SessionStateChanged(id, updated))
          actor.continue(CoordinatorState(..state, sessions: new_sessions))
        }
        Error(_) -> actor.continue(state)
      }
    }
  }
}

// Helper functions

fn notify_ui(state: CoordinatorState, msg: UiMsg) -> Nil {
  case state.ui_subject {
    Some(ui) -> process.send(ui, msg)
    None -> Nil
  }
}

fn spawn_beads_load(reply_to: Subject(Msg), project_path: String, config: Config) -> Nil {
  let _ = process.spawn(fn() {
    let result = beads.list_all(project_path, config)
    process.send(reply_to, BeadsLoaded(result))
  })
  Nil
}

/// Load beads for a specific project (runs in project directory)
fn spawn_beads_load_for_project(state: CoordinatorState, proj: Project) -> Nil {
  case state.self_subject {
    Some(reply_to) -> {
      let _ = process.spawn(fn() {
        // Run bd list in project directory
        let result = beads.list_all(proj.path, state.config)
        process.send(reply_to, BeadsLoaded(result))
      })
      Nil
    }
    None -> Nil
  }
}

/// Discover projects async
fn spawn_project_discovery(reply_to: Subject(Msg)) -> Nil {
  let _ = process.spawn(fn() {
    let projects = project_service.discover_all()
    process.send(reply_to, ProjectsDiscovered(projects))
  })
  Nil
}

/// Select initial project async
fn spawn_initial_project(reply_to: Subject(Msg)) -> Nil {
  let _ = process.spawn(fn() {
    let result = project_service.select_initial()
    process.send(reply_to, InitialProjectSelected(result))
  })
  Nil
}

/// Schedule periodic refresh tick
fn schedule_tick(reply_to: Subject(Msg)) -> Nil {
  let _ = process.spawn(fn() {
    process.sleep(refresh_interval_ms)
    process.send(reply_to, PeriodicTick)
  })
  Nil
}

fn next_status(current: task.Status, direction: Int) -> task.Status {
  let statuses = [task.Open, task.InProgress, task.Blocked, task.Done]
  let current_idx =
    list.index_map(statuses, fn(s, i) { #(s, i) })
    |> list.find(fn(pair) { pair.0 == current })
    |> result.map(fn(pair) { pair.1 })
    |> result.unwrap(0)

  let new_idx = int.clamp(current_idx + direction, 0, 3)
  case statuses |> list.drop(new_idx) |> list.first {
    Ok(s) -> s
    Error(_) -> current
  }
}

fn handle_start_session(
  state: CoordinatorState,
  id: String,
  with_work: Bool,
  yolo: Bool,
) -> CoordinatorState {
  let project_path = get_project_path(state)
  let worktree_result = worktree.ensure(id, state.config, project_path)
  case worktree_result {
    Ok(worktree_path) -> {
      let tmux_name = id <> "-az"

      case tmux.session_exists(tmux_name) {
        True -> {
          case tmux.attach(tmux_name) {
            Ok(_) -> state
            Error(e) -> {
              notify_ui(state, Toast("Failed to attach: " <> tmux.error_to_string(e), ErrorLevel))
              state
            }
          }
        }
        False -> {
          case tmux.new_session(tmux_name, worktree_path) {
            Ok(_) -> {
              // Run init commands - warn on failure but continue
              run_init_commands(tmux_name, state.config.worktree.init_commands, state)

              // Set init done marker - warn on failure but continue
              case tmux.set_option(tmux_name, "@az_init_done", "1") {
                Ok(_) -> Nil
                Error(e) -> {
                  notify_ui(state, Toast("Failed to set init marker: " <> tmux.error_to_string(e), Warning))
                }
              }

              // Start background tasks - warn on failure but continue
              list.each(state.config.session.background_tasks, fn(cmd) {
                let window_name =
                  "task-" <> int.to_string(int.absolute_value(unique_integer()))
                case tmux.new_window(tmux_name, window_name) {
                  Ok(_) -> {
                    case tmux.send_keys(tmux_name <> ":" <> window_name, cmd) {
                      Ok(_) -> {
                        case tmux.send_keys(tmux_name <> ":" <> window_name, "Enter") {
                          Ok(_) -> Nil
                          Error(e) -> {
                            notify_ui(state, Toast("Background task Enter failed: " <> tmux.error_to_string(e), Warning))
                          }
                        }
                      }
                      Error(e) -> {
                        notify_ui(state, Toast("Background task failed: " <> tmux.error_to_string(e), Warning))
                      }
                    }
                  }
                  Error(e) -> {
                    notify_ui(state, Toast("Failed to create task window: " <> tmux.error_to_string(e), Warning))
                  }
                }
              })

              let claude_cmd = case with_work, yolo {
                True, True -> build_start_work_command(id, state, True)
                True, False -> build_start_work_command(id, state, False)
                False, _ -> "claude"
              }

              // Start Claude - this is critical
              case tmux.send_keys(tmux_name <> ":main", claude_cmd) {
                Ok(_) -> {
                  case tmux.send_keys(tmux_name <> ":main", "Enter") {
                    Ok(_) -> {
                      let session_state =
                        session.SessionState(
                          bead_id: id,
                          state: session.Busy,
                          started_at: Some(now_iso()),
                          last_output: None,
                          worktree_path: Some(worktree_path),
                          tmux_session: Some(tmux_name),
                        )

                      let new_sessions = dict.insert(state.sessions, id, session_state)
                      notify_ui(state, SessionStateChanged(id, session_state))
                      notify_ui(state, Toast("Session started", Success))

                      CoordinatorState(..state, sessions: new_sessions)
                    }
                    Error(e) -> {
                      notify_ui(state, Toast("Failed to start Claude: " <> tmux.error_to_string(e), ErrorLevel))
                      state
                    }
                  }
                }
                Error(e) -> {
                  notify_ui(state, Toast("Failed to start Claude: " <> tmux.error_to_string(e), ErrorLevel))
                  state
                }
              }
            }
            Error(e) -> {
              notify_ui(state, Toast("Failed to create session: " <> tmux.error_to_string(e), ErrorLevel))
              state
            }
          }
        }
      }
    }
    Error(e) -> {
      notify_ui(state, Toast(worktree.error_to_string(e), ErrorLevel))
      state
    }
  }
}

fn ensure_session(state: CoordinatorState, id: String) -> CoordinatorState {
  case dict.get(state.sessions, id) {
    Ok(_) -> state
    Error(_) -> handle_start_session(state, id, False, False)
  }
}

fn start_dev_server(
  state: CoordinatorState,
  id: String,
  server_name: String,
) -> actor.Next(CoordinatorState, Msg) {
  case
    list.find(state.config.dev_server.servers, fn(s) { s.name == server_name })
  {
    Ok(server) -> {
      let tmux_name = id <> "-az"
      let window_name = "dev-" <> server_name
      let key = id <> ":" <> server_name

      // Get worktree path for this bead
      let worktree_path = case state.current_project {
        Some(proj) -> {
          let project_name = get_project_name(proj.path)
          let parent_dir = get_parent_dir(proj.path)
          Some(parent_dir <> "/" <> project_name <> "-" <> id)
        }
        None -> None
      }

      case tmux.new_window(tmux_name, window_name) {
        Ok(_) -> {
          // Allocate port using allocator (with conflict resolution)
          let base_port = get_server_base_port(server)
          let port = case state.port_allocator {
            Some(allocator) -> port_allocator.allocate(allocator, base_port, key)
            None -> base_port
          }

          // Build env string from all port configs
          let env_str = build_port_env_string(server.ports, port)
          let cmd = env_str <> " " <> server.command
          case tmux.send_keys(tmux_name <> ":" <> window_name, cmd) {
            Ok(_) -> {
              case tmux.send_keys(tmux_name <> ":" <> window_name, "Enter") {
                Ok(_) -> {
                  let ds =
                    DevServerState(
                      name: server_name,
                      status: dev_server_state.Starting,
                      port: Some(port),
                      window_name: window_name,
                      tmux_session: Some(tmux_name),
                      worktree_path: worktree_path,
                      started_at: Some(erlang_monotonic_time()),
                      error: None,
                    )
                  let new_servers = dict.insert(state.dev_servers, key, ds)
                  notify_ui(state, DevServerStateChanged(key, ds))
                  notify_ui(
                    state,
                    Toast("Dev server started on port " <> int.to_string(port), Success),
                  )
                  actor.continue(CoordinatorState(..state, dev_servers: new_servers))
                }
                Error(e) -> {
                  notify_ui(state, Toast("Failed to start dev server: " <> tmux.error_to_string(e), ErrorLevel))
                  actor.continue(state)
                }
              }
            }
            Error(e) -> {
              notify_ui(state, Toast("Failed to start dev server: " <> tmux.error_to_string(e), ErrorLevel))
              actor.continue(state)
            }
          }
        }
        Error(e) -> {
          notify_ui(state, Toast("Failed to create dev server window: " <> tmux.error_to_string(e), ErrorLevel))
          actor.continue(state)
        }
      }
    }
    Error(_) -> {
      notify_ui(state, Toast("Server config not found", ErrorLevel))
      actor.continue(state)
    }
  }
}

fn get_server_base_port(server: config.ServerDefinition) -> Int {
  case list.find(server.ports, fn(p) { p.0 == "PORT" }) {
    Ok(#(_, port)) -> port
    Error(_) -> 3000
  }
}

fn build_port_env_string(ports: List(#(String, Int)), allocated_port: Int) -> String {
  ports
  |> list.map(fn(p) {
    let #(name, _base) = p
    // Use allocated port for PORT, base port for others
    let port_value = case name {
      "PORT" -> allocated_port
      _ -> p.1
    }
    name <> "=" <> int.to_string(port_value)
  })
  |> string.join(" ")
}

fn get_project_name(path: String) -> String {
  path
  |> string.split("/")
  |> list.last
  |> result.unwrap("project")
}

fn get_parent_dir(path: String) -> String {
  path
  |> string.split("/")
  |> list.take(list.length(string.split(path, "/")) - 1)
  |> string.join("/")
}

@external(erlang, "erlang", "monotonic_time")
fn erlang_monotonic_time() -> Int

fn run_init_commands(tmux_name: String, commands: List(String), state: CoordinatorState) -> Nil {
  list.each(commands, fn(cmd) {
    case tmux.send_keys(tmux_name, cmd) {
      Ok(_) -> {
        case tmux.send_keys(tmux_name, "Enter") {
          Ok(_) -> Nil
          Error(e) -> {
            notify_ui(state, Toast("Init command Enter failed: " <> tmux.error_to_string(e), Warning))
          }
        }
      }
      Error(e) -> {
        notify_ui(state, Toast("Init command failed (" <> cmd <> "): " <> tmux.error_to_string(e), Warning))
      }
    }
    process.sleep(1000)
  })
}

fn build_start_work_command(
  id: String,
  state: CoordinatorState,
  yolo: Bool,
) -> String {
  let task_info = case list.find(state.tasks, fn(t) { t.id == id }) {
    Ok(t) -> task.issue_type_to_string(t.issue_type) <> ": " <> t.title
    Error(_) -> id
  }

  let prompt =
    "work on bead "
    <> id
    <> " ("
    <> task_info
    <> ")\\n\\nRun `bd show "
    <> id
    <> "` to see full description and context.\\n\\nBefore starting implementation:\\n1. If ANYTHING is unclear or underspecified, ASK ME questions before proceeding\\n2. Once you understand the task, update the bead with your implementation plan using `bd update "
    <> id
    <> " --design=\\\"...\\\"`\\n\\nGoal: Make this bead self-sufficient so any future session could pick it up without extra context."

  case yolo {
    True -> "claude --dangerously-skip-permissions -p \"" <> prompt <> "\""
    False -> "claude -p \"" <> prompt <> "\""
  }
}

fn now_iso() -> String {
  tempo.format_utc(tempo.ISO8601Seconds)
}

/// Remove image link from bead notes
fn remove_image_from_notes(
  issue_id: String,
  filename: String,
  project_path: String,
  config: Config,
) -> Result(Nil, beads.BeadsError) {
  case beads.show(issue_id, project_path, config) {
    Ok(t) -> {
      case t.notes {
        Some(notes) -> {
          let prefix = "📎 [" <> filename <> "]"
          let lines = string.split(notes, "\n")
          let filtered =
            list.filter(lines, fn(line) { !string.starts_with(line, prefix) })

          case list.length(filtered) == list.length(lines) {
            True -> Ok(Nil)
            False -> {
              let new_notes = string.join(filtered, "\n") |> string.trim
              beads.update_notes(issue_id, new_notes, project_path, config)
            }
          }
        }
        None -> Ok(Nil)
      }
    }
    Error(e) -> Error(e)
  }
}
