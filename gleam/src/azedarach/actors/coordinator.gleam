// Coordinator Actor - central orchestration
// Manages task cache, session registry, routes commands
// Handles project switching and periodic refresh

import gleam/dict.{type Dict}
import gleam/erlang
import gleam/erlang/process.{type Subject}
import gleam/int
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/otp/actor
import gleam/result
import gleam/string
import azedarach/config.{type Config}
import azedarach/domain/task.{type Task}
import azedarach/domain/session.{type SessionState, SessionState}
import azedarach/domain/project.{type Project}
import azedarach/services/beads
import azedarach/services/bead_editor
import azedarach/services/image
import azedarach/services/tmux
import azedarach/services/worktree
import azedarach/services/git
import azedarach/services/project_service

// Refresh interval in milliseconds (2 seconds)
const refresh_interval_ms = 2000

// Actor state
pub type CoordinatorState {
  CoordinatorState(
    config: Config,
    tasks: List(Task),
    sessions: Dict(String, SessionState),
    dev_servers: Dict(String, DevServerState),
    ui_subject: Option(Subject(UiMsg)),
    // Project management
    current_project: Option(Project),
    available_projects: List(Project),
    // Self reference for periodic refresh
    self_subject: Option(Subject(Msg)),
  )
}

pub type DevServerState {
  DevServerState(
    name: String,
    running: Bool,
    port: Option(Int),
    window_name: String,
  )
}

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
  // Dev servers
  ToggleDevServer(id: String, server: String)
  ViewDevServer(id: String, server: String)
  RestartDevServer(id: String, server: String)
  // Git
  UpdateFromMain(id: String)
  MergeToMain(id: String)
  CreatePR(id: String)
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
  Error
}

// Start the coordinator
pub fn start(config: Config) -> Result(Subject(Msg), actor.StartError) {
  actor.start_spec(actor.Spec(
    init: fn() {
      let state =
        CoordinatorState(
          config: config,
          tasks: [],
          sessions: dict.new(),
          dev_servers: dict.new(),
          ui_subject: None,
          current_project: None,
          available_projects: [],
          self_subject: None,
        )
      actor.Ready(state, process.new_selector())
    },
    init_timeout: 5000,
    loop: handle_message,
  ))
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
  msg: Msg,
  state: CoordinatorState,
) -> actor.Next(Msg, CoordinatorState) {
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
          notify_ui(state, Toast(project_service.error_to_string(e), Error))
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
              notify_ui(state, Toast(project_service.error_to_string(e), Error))
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
      spawn_beads_load(self, state.config)
      actor.continue(state)
    }

    BeadsLoaded(result) -> {
      case result {
        Ok(tasks) -> {
          notify_ui(state, TasksUpdated(tasks))
          actor.continue(CoordinatorState(..state, tasks: tasks))
        }
        Error(e) -> {
          notify_ui(state, Toast(beads.error_to_string(e), Error))
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
      case beads.create(options, state.config) {
        Ok(id) -> {
          notify_ui(state, Toast("Bead created: " <> id, Success))
          spawn_beads_load(process.new_subject(), state.config)
        }
        Error(e) -> notify_ui(state, Toast(beads.error_to_string(e), Error))
      }
      actor.continue(state)
    }

    CreateBeadViaEditor(issue_type) -> {
      case bead_editor.create_bead(issue_type, state.config) {
        Ok(parsed) -> {
          case bead_editor.create_from_parsed(parsed, issue_type, state.config) {
            Ok(id) -> {
              notify_ui(state, Toast("Bead created: " <> id, Success))
              spawn_beads_load(process.new_subject(), state.config)
            }
            Error(e) ->
              notify_ui(state, Toast(bead_editor.error_to_string(e), Error))
          }
        }
        Error(e) -> notify_ui(state, Toast(bead_editor.error_to_string(e), Error))
      }
      actor.continue(state)
    }

    EditBead(id) -> {
      case bead_editor.edit_bead(id, state.config) {
        Ok(parsed) -> {
          case bead_editor.apply_changes(id, parsed, state.config) {
            Ok(_) -> {
              notify_ui(state, Toast("Bead updated", Success))
              spawn_beads_load(process.new_subject(), state.config)
            }
            Error(e) ->
              notify_ui(state, Toast(bead_editor.error_to_string(e), Error))
          }
        }
        Error(e) -> notify_ui(state, Toast(bead_editor.error_to_string(e), Error))
      }
      actor.continue(state)
    }

    DeleteBead(id) -> {
      case beads.delete(id, state.config) {
        Ok(_) -> {
          notify_ui(state, Toast("Bead deleted", Success))
          spawn_beads_load(process.new_subject(), state.config)
        }
        Error(e) -> notify_ui(state, Toast(beads.error_to_string(e), Error))
      }
      actor.continue(state)
    }

    MoveTask(id, direction) -> {
      case list.find(state.tasks, fn(t) { t.id == id }) {
        Ok(found_task) -> {
          let new_status = next_status(found_task.status, direction)
          case beads.update_status(id, new_status, state.config) {
            Ok(_) -> spawn_beads_load(process.new_subject(), state.config)
            Error(e) -> notify_ui(state, Toast(beads.error_to_string(e), Error))
          }
        }
        Error(_) -> Nil
      }
      actor.continue(state)
    }

    SearchBeads(pattern) -> {
      case beads.search(pattern, state.config) {
        Ok(results) -> notify_ui(state, SearchResults(results))
        Error(e) -> notify_ui(state, Toast(beads.error_to_string(e), Error))
      }
      actor.continue(state)
    }

    GetReadyBeads -> {
      case beads.ready(state.config) {
        Ok(results) -> notify_ui(state, SearchResults(results))
        Error(e) -> notify_ui(state, Toast(beads.error_to_string(e), Error))
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
                  tmux.attach(tmux_name)
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
              tmux.send_keys(tmux_name, "C-c")
              case session_state.worktree_path {
                Some(path) -> {
                  let _ = git.wip_commit(path)
                  Nil
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
              tmux.send_keys(tmux_name, "claude")
              tmux.send_keys(tmux_name, "Enter")
              let new_session =
                session.SessionState(..session_state, state: session.Busy)
              let new_sessions = dict.insert(state.sessions, id, new_session)
              notify_ui(state, SessionStateChanged(id, new_session))
              actor.continue(
                CoordinatorState(..state, sessions: new_sessions),
              )
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
              tmux.kill_session(tmux_name)
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
                  tmux.attach(tmux_name)
                }
                Error(git.MergeConflict(files)) -> {
                  let tmux_name =
                    session_state.tmux_session |> option.unwrap(id <> "-az")
                  tmux.new_window(tmux_name, "merge")
                  let prompt =
                    "There are merge conflicts in: "
                    <> string.join(files, ", ")
                    <> ". Please resolve these conflicts, then stage and commit the resolution."
                  tmux.send_keys(
                    tmux_name <> ":merge",
                    "claude -p \"" <> prompt <> "\"",
                  )
                  tmux.send_keys(tmux_name <> ":merge", "Enter")
                  notify_ui(
                    state,
                    Toast(
                      "Conflicts detected. Claude started in 'merge' window.",
                      Warning,
                    ),
                  )
                }
                Error(e) -> notify_ui(state, Toast(git.error_to_string(e), Error))
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
        Ok(ds) if ds.running -> {
          let window = ds.window_name
          tmux.kill_window(id <> "-az", window)
          let new_ds = DevServerState(..ds, running: False, port: None)
          let new_servers = dict.insert(state.dev_servers, key, new_ds)
          notify_ui(state, DevServerStateChanged(key, new_ds))
          actor.continue(CoordinatorState(..state, dev_servers: new_servers))
        }
        _ -> {
          let new_state = ensure_session(state, id)
          start_dev_server(new_state, id, server_name)
        }
      }
    }

    ViewDevServer(id, server_name) -> {
      let key = id <> ":" <> server_name
      case dict.get(state.dev_servers, key) {
        Ok(ds) -> tmux.select_window(id <> "-az", ds.window_name)
        Error(_) -> notify_ui(state, Toast("Dev server not running", Warning))
      }
      actor.continue(state)
    }

    RestartDevServer(id, server_name) -> {
      let key = id <> ":" <> server_name
      case dict.get(state.dev_servers, key) {
        Ok(ds) -> {
          tmux.send_keys(id <> "-az:" <> ds.window_name, "C-c")
          process.sleep(100)
          case
            list.find(state.config.dev_server.servers, fn(s) {
              s.name == server_name
            })
          {
            Ok(server) -> {
              tmux.send_keys(id <> "-az:" <> ds.window_name, server.command)
              tmux.send_keys(id <> "-az:" <> ds.window_name, "Enter")
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
                  tmux.new_window(tmux_name, "merge")
                  let prompt =
                    "There are merge conflicts. Please resolve: "
                    <> string.join(files, ", ")
                  tmux.send_keys(
                    tmux_name <> ":merge",
                    "claude -p \"" <> prompt <> "\"",
                  )
                  tmux.send_keys(tmux_name <> ":merge", "Enter")
                  notify_ui(
                    state,
                    Toast("Conflicts detected. Claude resolving.", Warning),
                  )
                }
                Error(e) -> notify_ui(state, Toast(git.error_to_string(e), Error))
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
                Error(e) -> notify_ui(state, Toast(git.error_to_string(e), Error))
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
                Error(e) -> notify_ui(state, Toast(git.error_to_string(e), Error))
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
      case dict.get(state.sessions, id) {
        Ok(session_state) -> {
          case session_state.tmux_session {
            Some(tmux_name) -> tmux.kill_session(tmux_name)
            None -> Nil
          }
          case session_state.worktree_path {
            Some(path) -> {
              let _ = worktree.delete(path)
              let _ = git.delete_branch(id, state.config)
              Nil
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
      case image.attach_from_clipboard(id) {
        Ok(attachment) -> {
          let link = image.build_notes_link(id, attachment)
          case beads.append_notes(id, link, state.config) {
            Ok(_) ->
              notify_ui(
                state,
                Toast("Image attached: " <> attachment.filename, Success),
              )
            Error(e) -> notify_ui(state, Toast(beads.error_to_string(e), Error))
          }
        }
        Error(e) -> notify_ui(state, Toast(image.error_to_string(e), Error))
      }
      actor.continue(state)
    }

    DeleteImage(id, attachment_id) -> {
      case image.remove(id, attachment_id) {
        Ok(attachment) -> {
          case remove_image_from_notes(id, attachment.filename, state.config) {
            Ok(_) -> notify_ui(state, Toast("Image deleted", Success))
            Error(e) -> notify_ui(state, Toast(beads.error_to_string(e), Error))
          }
        }
        Error(e) -> notify_ui(state, Toast(image.error_to_string(e), Error))
      }
      actor.continue(state)
    }

    AddDependency(id, depends_on, dep_type) -> {
      case beads.add_dependency(id, depends_on, dep_type, state.config) {
        Ok(_) -> {
          notify_ui(state, Toast("Dependency added", Success))
          spawn_beads_load(process.new_subject(), state.config)
        }
        Error(e) -> notify_ui(state, Toast(beads.error_to_string(e), Error))
      }
      actor.continue(state)
    }

    RemoveDependency(id, depends_on) -> {
      case beads.remove_dependency(id, depends_on, state.config) {
        Ok(_) -> {
          notify_ui(state, Toast("Dependency removed", Success))
          spawn_beads_load(process.new_subject(), state.config)
        }
        Error(e) -> notify_ui(state, Toast(beads.error_to_string(e), Error))
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

fn spawn_beads_load(reply_to: Subject(Msg), config: Config) -> Nil {
  process.start(
    fn() {
      let result = beads.list_all(config)
      process.send(reply_to, BeadsLoaded(result))
    },
    True,
  )
  Nil
}

/// Load beads for a specific project (runs in project directory)
fn spawn_beads_load_for_project(state: CoordinatorState, proj: Project) -> Nil {
  case state.self_subject {
    Some(reply_to) -> {
      process.start(
        fn() {
          // Run bd list in project directory
          let result = beads.list_all_in_dir(proj.path, state.config)
          process.send(reply_to, BeadsLoaded(result))
        },
        True,
      )
      Nil
    }
    None -> Nil
  }
}

/// Discover projects async
fn spawn_project_discovery(reply_to: Subject(Msg)) -> Nil {
  process.start(
    fn() {
      let projects = project_service.discover_all()
      process.send(reply_to, ProjectsDiscovered(projects))
    },
    True,
  )
  Nil
}

/// Select initial project async
fn spawn_initial_project(reply_to: Subject(Msg)) -> Nil {
  process.start(
    fn() {
      let result = project_service.select_initial()
      process.send(reply_to, InitialProjectSelected(result))
    },
    True,
  )
  Nil
}

/// Schedule periodic refresh tick
fn schedule_tick(reply_to: Subject(Msg)) -> Nil {
  process.start(
    fn() {
      process.sleep(refresh_interval_ms)
      process.send(reply_to, PeriodicTick)
    },
    True,
  )
  Nil
}

fn next_status(current: task.Status, direction: Int) -> task.Status {
  let statuses = [task.Open, task.InProgress, task.Review, task.Done]
  let current_idx =
    list.index_map(statuses, fn(s, i) { #(s, i) })
    |> list.find(fn(pair) { pair.0 == current })
    |> result.map(fn(pair) { pair.1 })
    |> result.unwrap(0)

  let new_idx = int.clamp(current_idx + direction, 0, 3)
  case list.at(statuses, new_idx) {
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
  let worktree_result = worktree.ensure(id, state.config)
  case worktree_result {
    Ok(worktree_path) -> {
      let tmux_name = id <> "-az"

      case tmux.session_exists(tmux_name) {
        True -> {
          tmux.attach(tmux_name)
          state
        }
        False -> {
          tmux.new_session(tmux_name, worktree_path)

          run_init_commands(tmux_name, state.config.worktree.init_commands)
          tmux.set_option(tmux_name, "@az_init_done", "1")

          list.each(state.config.session.background_tasks, fn(cmd) {
            let window_name =
              "task-" <> int.to_string(erlang.unique_integer([positive]))
            tmux.new_window(tmux_name, window_name)
            tmux.send_keys(tmux_name <> ":" <> window_name, cmd)
            tmux.send_keys(tmux_name <> ":" <> window_name, "Enter")
          })

          let claude_cmd = case with_work, yolo {
            True, True -> build_start_work_command(id, state, True)
            True, False -> build_start_work_command(id, state, False)
            False, _ -> "claude"
          }
          tmux.send_keys(tmux_name <> ":main", claude_cmd)
          tmux.send_keys(tmux_name <> ":main", "Enter")

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
      }
    }
    Error(e) -> {
      notify_ui(state, Toast(worktree.error_to_string(e), Error))
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
) -> actor.Next(Msg, CoordinatorState) {
  case
    list.find(state.config.dev_server.servers, fn(s) { s.name == server_name })
  {
    Ok(server) -> {
      let tmux_name = id <> "-az"
      let window_name = "dev-" <> server_name

      tmux.new_window(tmux_name, window_name)

      let port = get_server_port(server)
      let cmd = "PORT=" <> int.to_string(port) <> " " <> server.command
      tmux.send_keys(tmux_name <> ":" <> window_name, cmd)
      tmux.send_keys(tmux_name <> ":" <> window_name, "Enter")

      let key = id <> ":" <> server_name
      let ds =
        DevServerState(
          name: server_name,
          running: True,
          port: Some(port),
          window_name: window_name,
        )
      let new_servers = dict.insert(state.dev_servers, key, ds)
      notify_ui(state, DevServerStateChanged(key, ds))
      notify_ui(
        state,
        Toast("Dev server started on port " <> int.to_string(port), Success),
      )

      actor.continue(CoordinatorState(..state, dev_servers: new_servers))
    }
    Error(_) -> {
      notify_ui(state, Toast("Server config not found", Error))
      actor.continue(state)
    }
  }
}

fn get_server_port(server: config.ServerDefinition) -> Int {
  case list.find(server.ports, fn(p) { p.0 == "PORT" }) {
    Ok(#(_, port)) -> port
    Error(_) -> 3000
  }
}

fn run_init_commands(tmux_name: String, commands: List(String)) -> Nil {
  list.each(commands, fn(cmd) {
    tmux.send_keys(tmux_name, cmd)
    tmux.send_keys(tmux_name, "Enter")
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
  // Simplified - real impl would use datetime library
  "2025-01-01T00:00:00Z"
}

/// Remove image link from bead notes
fn remove_image_from_notes(
  issue_id: String,
  filename: String,
  config: Config,
) -> Result(Nil, beads.BeadsError) {
  case beads.show(issue_id, config) {
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
              beads.update_notes(issue_id, new_notes, config)
            }
          }
        }
        None -> Ok(Nil)
      }
    }
    Error(e) -> Error(e)
  }
}
