// Application Supervisor
// Top-level OTP supervisor using one_for_one strategy
// Manages: Shore App, Coordinator, Sessions Supervisor, Servers Supervisor

import gleam/erlang/process.{type Subject}
import gleam/option.{type Option, None, Some}
import gleam/otp/actor
import gleam/result
import azedarach/config.{type Config}
import azedarach/domain/session
import azedarach/services/dev_server_state
import azedarach/actors/coordinator
import azedarach/actors/sessions_sup
import azedarach/actors/servers_sup

/// Application context containing all supervised components
pub type AppContext {
  AppContext(
    config: Config,
    coordinator: Subject(coordinator.Msg),
    sessions_supervisor: Subject(sessions_sup.Msg),
    servers_supervisor: Subject(servers_sup.Msg),
  )
}

/// Supervisor state
pub type SupervisorState {
  SupervisorState(
    config: Config,
    coordinator: Option(Subject(coordinator.Msg)),
    sessions_sup: Option(Subject(sessions_sup.Msg)),
    servers_sup: Option(Subject(servers_sup.Msg)),
    // Callback for when all children are ready
    on_ready: Option(Subject(AppContext)),
  )
}

/// Messages for the supervisor
pub type Msg {
  /// Child started successfully
  ChildStarted(child: ChildType)
  /// Child failed to start
  ChildFailed(child: ChildType, reason: String)
  /// Request shutdown
  Shutdown
}

/// Child types
pub type ChildType {
  CoordinatorChild(Subject(coordinator.Msg))
  SessionsSupChild(Subject(sessions_sup.Msg))
  ServersSupChild(Subject(servers_sup.Msg))
}

/// Start the application supervisor
/// Returns the app context once all children are started
pub fn start(config: Config) -> Result(AppContext, StartError) {
  let ready_subject = process.new_subject()

  // Start the supervisor actor
  case start_supervisor(config, ready_subject) {
    Ok(_supervisor) -> {
      // Wait for all children to be ready
      case process.receive(ready_subject, 30_000) {
        Ok(context) -> Ok(context)
        Error(_) -> Error(TimeoutWaitingForChildren)
      }
    }
    Error(e) -> Error(SupervisorStartFailed(e))
  }
}

/// Errors during startup
pub type StartError {
  SupervisorStartFailed(actor.StartError)
  TimeoutWaitingForChildren
  ChildStartFailed(String)
}

/// Start the supervisor actor
fn start_supervisor(
  config: Config,
  on_ready: Subject(AppContext),
) -> Result(Subject(Msg), actor.StartError) {
  let initial_state =
    SupervisorState(
      config: config,
      coordinator: None,
      sessions_sup: None,
      servers_sup: None,
      on_ready: Some(on_ready),
    )

  let start_result =
    actor.new(initial_state)
    |> actor.on_message(handle_message)
    |> actor.start
    |> result.map(fn(started) {
      let actor.Started(_, data) = started
      data
    })

  // Start children after supervisor is running
  case start_result {
    Ok(supervisor) -> {
      start_children(config, supervisor)
      Ok(supervisor)
    }
    Error(e) -> Error(e)
  }
}

/// Start all children asynchronously
/// Order: Coordinator first, then supervisors (so we can wire callbacks properly)
fn start_children(config: Config, supervisor: Subject(Msg)) -> Nil {
  // Start each child in a separate process
  let _ = process.spawn(fn() {
    // Start Coordinator FIRST so we have its subject for callbacks
    case coordinator.start(config) {
      Ok(coord) -> {
        process.send(supervisor, ChildStarted(CoordinatorChild(coord)))

        // Create bridge subjects that forward monitor updates to coordinator
        let sessions_callback = create_sessions_coordinator_bridge(coord)
        let servers_callback = create_servers_coordinator_bridge(coord)

        // Start Sessions Supervisor with proper callback
        case sessions_sup.start(sessions_callback) {
          Ok(sessions) -> {
            process.send(supervisor, ChildStarted(SessionsSupChild(sessions)))
          }
          Error(_) -> {
            process.send(supervisor, ChildFailed(SessionsSupChild(process.new_subject()), "Failed to start sessions supervisor"))
          }
        }

        // Start Servers Supervisor with proper callback
        case servers_sup.start(servers_callback) {
          Ok(servers) -> {
            process.send(supervisor, ChildStarted(ServersSupChild(servers)))
          }
          Error(_) -> {
            process.send(supervisor, ChildFailed(ServersSupChild(process.new_subject()), "Failed to start servers supervisor"))
          }
        }
      }
      Error(_) -> {
        process.send(supervisor, ChildFailed(CoordinatorChild(process.new_subject()), "Failed to start coordinator"))
      }
    }
  })
  Nil
}

/// Create a bridge subject that forwards sessions_sup.CoordinatorUpdate to coordinator
fn create_sessions_coordinator_bridge(
  coord: Subject(coordinator.Msg),
) -> Subject(sessions_sup.CoordinatorUpdate) {
  let bridge_subject = process.new_subject()

  // Spawn bridge process that converts and forwards messages
  let _ = process.spawn(fn() {
    forward_session_updates(bridge_subject, coord)
  })

  bridge_subject
}

/// Forward session monitor updates to coordinator
fn forward_session_updates(
  from: Subject(sessions_sup.CoordinatorUpdate),
  to: Subject(coordinator.Msg),
) -> Nil {
  case process.receive(from, 60_000) {
    Ok(sessions_sup.SessionStateChanged(bead_id, state)) -> {
      // Convert sessions_sup message to coordinator message
      process.send(to, coordinator.SessionMonitorUpdate(bead_id, state))
      forward_session_updates(from, to)
    }
    Ok(sessions_sup.SessionMarkedUnknown(bead_id, _reason)) -> {
      // Mark session as Unknown state
      process.send(to, coordinator.SessionMonitorUpdate(bead_id, session.Unknown))
      forward_session_updates(from, to)
    }
    Error(_) -> {
      // Timeout, continue listening
      forward_session_updates(from, to)
    }
  }
}

/// Create a bridge subject that forwards servers_sup.CoordinatorUpdate to coordinator
fn create_servers_coordinator_bridge(
  coord: Subject(coordinator.Msg),
) -> Subject(servers_sup.CoordinatorUpdate) {
  let bridge_subject = process.new_subject()

  // Spawn bridge process that converts and forwards messages
  let _ = process.spawn(fn() {
    forward_server_updates(bridge_subject, coord)
  })

  bridge_subject
}

/// Forward server monitor updates to coordinator
fn forward_server_updates(
  from: Subject(servers_sup.CoordinatorUpdate),
  to: Subject(coordinator.Msg),
) -> Nil {
  case process.receive(from, 60_000) {
    Ok(servers_sup.ServerStatusChanged(bead_id, server_name, dev_state)) -> {
      // Forward the DevServerState to coordinator
      let key = dev_server_state.make_key(bead_id, server_name)
      process.send(to, coordinator.DevServerUpdate(key, dev_state))
      forward_server_updates(from, to)
    }
    Ok(servers_sup.ServerMarkedUnknown(bead_id, server_name, reason)) -> {
      // Create error state and forward
      let key = dev_server_state.make_key(bead_id, server_name)
      let error_state = dev_server_state.DevServerState(
        name: server_name,
        status: dev_server_state.Error(reason),
        port: None,
        window_name: server_name,
        tmux_session: None,
        worktree_path: None,
        started_at: None,
        error: Some(reason),
      )
      process.send(to, coordinator.DevServerUpdate(key, error_state))
      forward_server_updates(from, to)
    }
    Error(_) -> {
      // Timeout, continue listening
      forward_server_updates(from, to)
    }
  }
}

/// Handle supervisor messages
fn handle_message(
  state: SupervisorState,
  msg: Msg,
) -> actor.Next(SupervisorState, Msg) {
  case msg {
    ChildStarted(child) -> {
      let new_state = case child {
        CoordinatorChild(subj) ->
          SupervisorState(..state, coordinator: Some(subj))
        SessionsSupChild(subj) ->
          SupervisorState(..state, sessions_sup: Some(subj))
        ServersSupChild(subj) ->
          SupervisorState(..state, servers_sup: Some(subj))
      }

      // Check if all children are ready
      case all_children_ready(new_state) {
        True -> {
          notify_ready(new_state)
          actor.continue(new_state)
        }
        False -> actor.continue(new_state)
      }
    }

    ChildFailed(_child, _reason) -> {
      // In a real impl, we'd handle restart logic here
      // For now, just continue - the child will be missing
      actor.continue(state)
    }

    Shutdown -> {
      // Stop all children gracefully
      actor.stop()
    }
  }
}

/// Check if all children are ready
fn all_children_ready(state: SupervisorState) -> Bool {
  option.is_some(state.coordinator)
  && option.is_some(state.sessions_sup)
  && option.is_some(state.servers_sup)
}

/// Notify that all children are ready
fn notify_ready(state: SupervisorState) -> Nil {
  case state.on_ready, state.coordinator, state.sessions_sup, state.servers_sup {
    Some(callback), Some(coord), Some(sessions), Some(servers) -> {
      let context =
        AppContext(
          config: state.config,
          coordinator: coord,
          sessions_supervisor: sessions,
          servers_supervisor: servers,
        )
      process.send(callback, context)
    }
    _, _, _, _ -> Nil
  }
}

/// Convenience function to initialize coordinator after supervisor starts
pub fn initialize_coordinator(context: AppContext) -> Nil {
  coordinator.initialize(context.coordinator)
}

/// Start a session monitor through the sessions supervisor
pub fn start_session_monitor(
  context: AppContext,
  bead_id: String,
  tmux_session: String,
) -> Nil {
  sessions_sup.start_monitor(
    context.sessions_supervisor,
    bead_id,
    tmux_session,
    None,
  )
}

/// Stop a session monitor
pub fn stop_session_monitor(context: AppContext, bead_id: String) -> Nil {
  sessions_sup.stop_monitor(context.sessions_supervisor, bead_id)
}

/// Start a server monitor through the servers supervisor
pub fn start_server_monitor(
  context: AppContext,
  bead_id: String,
  server_name: String,
  tmux_session: String,
  window_name: String,
  port: Option(Int),
) -> Nil {
  servers_sup.start_monitor(
    context.servers_supervisor,
    bead_id,
    server_name,
    tmux_session,
    window_name,
    port,
    None,
    None,
    None,
  )
}

/// Stop a server monitor
pub fn stop_server_monitor(
  context: AppContext,
  bead_id: String,
  server_name: String,
) -> Nil {
  servers_sup.stop_monitor(context.servers_supervisor, bead_id, server_name)
}

/// Error to string
pub fn error_to_string(err: StartError) -> String {
  case err {
    SupervisorStartFailed(_) -> "Failed to start application supervisor"
    TimeoutWaitingForChildren -> "Timeout waiting for child processes to start"
    ChildStartFailed(reason) -> "Child failed to start: " <> reason
  }
}
