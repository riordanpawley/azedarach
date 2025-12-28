// Shore TUI Application
// Main application setup and lifecycle
//
// All side effects go through Shore's effect system.
// See effects.gleam for effect helpers.

import gleam/erlang/process.{type Subject}
import gleam/otp/actor
import shore.{type Event}
import azedarach/config.{type Config}
import azedarach/ui/model.{type Model, type Msg}
import azedarach/ui/update
import azedarach/ui/view
import azedarach/ui/theme
import azedarach/ui/effects.{type Effect}
import azedarach/actors/coordinator
import azedarach/actors/app_supervisor.{type AppContext}
import azedarach/services/dev_server_state

/// Start with existing supervision context (preferred method)
pub fn start_with_context(context: AppContext) -> Result(Nil, actor.StartError) {
  // Initialize theme
  let colors = theme.load(context.config.theme)

  // Create exit subject for graceful shutdown
  let exit = process.new_subject()

  // Configure and start Shore with supervision context
  let spec =
    shore.spec(
      init: fn() { init_with_context(context.config, colors, context, exit) },
      view: view.render,
      update: fn(model, msg) { update.update_with_context(model, msg, context) },
      exit: exit,
      keybinds: shore.default_keybinds(),
      redraw: shore.on_timer(16),
    )

  // Start the application and set up coordinator subscription
  case shore.start(spec) {
    Ok(shore_subject) -> {
      // Subscribe to coordinator for UiMsg notifications
      start_ui_bridge(shore_subject, context.coordinator)
      // Block until exit message is received (keeps BEAM VM alive)
      let _ = process.receive_forever(exit)
      Ok(Nil)
    }
    Error(e) -> Error(e)
  }
}

/// Legacy start function (starts its own coordinator)
/// Deprecated: Use start_with_context instead
pub fn start(config: Config) -> Result(Nil, actor.StartError) {
  // Initialize theme
  let colors = theme.load(config.theme)

  // Start coordinator actor (legacy - not supervised)
  let assert Ok(coord) = coordinator.start(config)

  // Create exit subject for graceful shutdown
  let exit = process.new_subject()

  // Configure and start Shore
  let spec =
    shore.spec(
      init: fn() { init(config, colors, coord) },
      view: view.render,
      update: fn(model, msg) { update.update(model, msg, coord) },
      exit: exit,
      keybinds: shore.default_keybinds(),
      redraw: shore.on_timer(16),
    )

  // Start the application and set up coordinator subscription
  case shore.start(spec) {
    Ok(shore_subject) -> {
      // Subscribe to coordinator for UiMsg notifications
      start_ui_bridge(shore_subject, coord)
      // Block until exit message is received (keeps BEAM VM alive)
      let _ = process.receive_forever(exit)
      Ok(Nil)
    }
    Error(e) -> Error(e)
  }
}

// =============================================================================
// UI Bridge - forwards coordinator UiMsg to Shore
// =============================================================================

/// Start the UI bridge that translates coordinator UiMsg to Shore Msg
fn start_ui_bridge(
  shore_subject: Subject(Event(Msg)),
  coord: Subject(coordinator.Msg),
) -> Nil {
  // Create a subject to receive UiMsg from coordinator
  let ui_receiver: Subject(coordinator.UiMsg) = process.new_subject()

  // Subscribe to coordinator
  coordinator.send(coord, coordinator.Subscribe(ui_receiver))

  // Spawn bridge process that forwards messages
  let _ = process.spawn(fn() {
    ui_bridge_loop(shore_subject, ui_receiver)
  })

  Nil
}

/// Bridge loop - receives UiMsg and forwards translated Msg to Shore
fn ui_bridge_loop(
  shore_subject: Subject(Event(Msg)),
  ui_receiver: Subject(coordinator.UiMsg),
) -> Nil {
  // Wait for UiMsg from coordinator
  case process.receive_forever(ui_receiver) {
    msg -> {
      // Translate and forward to Shore (wrap with shore.send)
      case translate_ui_msg(msg) {
        Ok(shore_msg) -> process.send(shore_subject, shore.send(shore_msg))
        Error(Nil) -> Nil
      }
      // Continue loop
      ui_bridge_loop(shore_subject, ui_receiver)
    }
  }
}

/// Translate coordinator UiMsg to model Msg
fn translate_ui_msg(msg: coordinator.UiMsg) -> Result(Msg, Nil) {
  case msg {
    coordinator.TasksUpdated(tasks) -> Ok(model.BeadsLoaded(tasks))

    coordinator.SearchResults(tasks) -> Ok(model.BeadsLoaded(tasks))

    coordinator.SessionStateChanged(id, state) ->
      Ok(model.SessionStateChanged(id, state))

    coordinator.DevServerStateChanged(id, state) ->
      Ok(model.DevServerStateChanged(
        id,
        model.DevServerState(
          name: state.name,
          running: dev_server_state.is_running(state),
          port: state.port,
          window_name: state.window_name,
        ),
      ))

    coordinator.Toast(message, level) -> {
      let model_level = effects.coordinator_to_model_toast_level(level)
      Ok(model.ShowToast(model_level, message))
    }

    coordinator.RequestMergeChoice(bead_id, behind_count, merge_in_progress) ->
      Ok(model.RequestMergeChoice(bead_id, behind_count, merge_in_progress))

    coordinator.ProjectChanged(project) ->
      Ok(model.ProjectChanged(project))

    coordinator.ProjectsUpdated(projects) ->
      Ok(model.ProjectsUpdated(projects))

    coordinator.PlanningStateUpdated(planning_state) ->
      translate_planning_state(planning_state)
  }
}

/// Translate coordinator PlanningState to model PlanningOverlayState
fn translate_planning_state(state: coordinator.PlanningState) -> Result(Msg, Nil) {
  case state {
    coordinator.PlanningIdle -> Error(Nil)
    coordinator.PlanningGenerating(desc) ->
      Ok(model.PlanningStateUpdated(model.PlanningGenerating(desc)))
    coordinator.PlanningReviewing(desc, pass, max_passes) ->
      Ok(model.PlanningStateUpdated(model.PlanningReviewing(desc, pass, max_passes)))
    coordinator.PlanningCreatingBeads(desc) ->
      Ok(model.PlanningStateUpdated(model.PlanningCreatingBeads(desc)))
    coordinator.PlanningComplete(ids) ->
      Ok(model.PlanningStateUpdated(model.PlanningComplete(ids)))
    coordinator.PlanningError(message) ->
      Ok(model.PlanningStateUpdated(model.PlanningError(message)))
  }
}

/// Initialize the model with supervision context
fn init_with_context(
  config: Config,
  colors: theme.Colors,
  context: AppContext,
  exit_subj: Subject(Nil),
) -> #(Model, Effect(Msg)) {
  let initial_model = model.init_with_context(config, colors, context, exit_subj)

  // Request initial beads load via Shore effects
  #(initial_model, effects.refresh_beads(context.coordinator))
}

/// Initialize the model and trigger initial effects (legacy)
fn init(
  config: Config,
  colors: theme.Colors,
  coord: Subject(coordinator.Msg),
) -> #(Model, Effect(Msg)) {
  let initial_model = model.init(config, colors)

  // Request initial beads load via Shore effects
  #(initial_model, effects.refresh_beads(coord))
}
