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

// Shore app configuration
pub type App {
  App(config: Config, coordinator: Subject(coordinator.Msg))
}

pub fn start(config: Config) -> Nil {
  // Initialize theme
  let colors = theme.load(config.theme)

  // Start coordinator actor
  let assert Ok(coord) = coordinator.start(config)

  // Create initial model
  let initial_model = model.init(config, colors)

  // Start Shore TUI
  // Note: Shore API is subject to change - this is the expected pattern
  run_tui(initial_model, coord)
}

fn run_tui(initial: Model, coord: Subject(coordinator.Msg)) -> Nil {
  // Shore's main loop - TEA pattern
  // This will be refined based on Shore's actual API
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
  // ... more commands
}

pub type Element =
  Dynamic

fn request_beads(coord: Subject(coordinator.Msg)) -> Cmd {
  coordinator.send(coord, coordinator.RefreshBeads)
  None
}
