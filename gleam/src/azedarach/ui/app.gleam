// Shore TUI Application
// Main application setup and lifecycle

import gleam/erlang/process.{type Subject}
import gleam/otp/actor
import azedarach/config.{type Config}
import azedarach/ui/model.{type Model, type Msg}
import azedarach/ui/update
import azedarach/ui/view
import azedarach/ui/theme
import azedarach/actors/coordinator
import azedarach/actors/app_supervisor.{type AppContext}

// Shore app configuration
pub type App {
  App(
    config: Config,
    coordinator: Subject(coordinator.Msg),
    context: AppContext,
  )
}

/// Start with existing supervision context (preferred method)
pub fn start_with_context(context: AppContext) -> Nil {
  // Initialize theme
  let colors = theme.load(context.config.theme)

  // Create initial model with app context
  let initial_model = model.init_with_context(context.config, colors, context)

  // Start Shore TUI
  run_tui_with_context(initial_model, context)
}

/// Legacy start function (starts its own coordinator)
/// Deprecated: Use start_with_context instead
pub fn start(config: Config) -> Nil {
  // Initialize theme
  let colors = theme.load(config.theme)

  // Start coordinator actor (legacy - not supervised)
  let assert Ok(coord) = coordinator.start(config)

  // Create initial model
  let initial_model = model.init(config, colors)

  // Start Shore TUI
  run_tui(initial_model, coord)
}

fn run_tui_with_context(initial: Model, context: AppContext) -> Nil {
  // Shore's main loop - TEA pattern
  shore_run(
    init: fn() { #(initial, request_beads(context.coordinator)) },
    update: fn(model, msg) {
      update.update_with_context(model, msg, context)
    },
    view: fn(model) { view.render(model) },
  )
}

fn run_tui(initial: Model, coord: Subject(coordinator.Msg)) -> Nil {
  // Shore's main loop - TEA pattern (legacy)
  shore_run(
    init: fn() { #(initial, request_beads(coord)) },
    update: fn(model, msg) { update.update(model, msg, coord) },
    view: fn(model) { view.render(model) },
  )
}

// External binding to Shore's run function
// Actual signature depends on Shore version
@external(erlang, "shore", "run")
fn shore_run(
  init init: fn() -> #(Model, Cmd),
  update update: fn(Model, Msg) -> #(Model, Cmd),
  view view: fn(Model) -> Element,
) -> Nil

// Command type (Shore's effect system)
pub type Cmd {
  None
  Batch(List(Cmd))
  RequestBeads
  StartSession(bead_id: String)
  StartSessionMonitor(bead_id: String, tmux_session: String)
  StopSessionMonitor(bead_id: String)
  StartServerMonitor(bead_id: String, server_name: String, tmux_session: String, window_name: String)
  StopServerMonitor(bead_id: String, server_name: String)
}

pub type Element =
  Dynamic

fn request_beads(coord: Subject(coordinator.Msg)) -> Cmd {
  coordinator.send(coord, coordinator.RefreshBeads)
  None
}
