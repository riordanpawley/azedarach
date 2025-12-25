// Shore TUI Application
// Main application setup and lifecycle

import gleam/erlang/process.{type Subject}
import gleam/list
import gleam/result
import shore
import azedarach/config.{type Config}
import azedarach/ui/model.{type Model, type Msg}
import azedarach/ui/update
import azedarach/ui/view
import azedarach/ui/theme
import azedarach/actors/coordinator

/// Effect type for Shore - a list of functions that return messages
pub type Effect(msg) =
  List(fn() -> msg)

/// No effects
pub fn none() -> Effect(msg) {
  []
}

/// Batch multiple effects
pub fn batch(effects: List(Effect(msg))) -> Effect(msg) {
  effects
  |> list.flatten
}

/// Start the TUI application
pub fn start(config: Config) -> Result(Nil, shore.StartError) {
  // Initialize theme
  let colors = theme.load(config.theme)

  // Start coordinator actor
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

  // Start the application
  shore.start(spec)
  |> result.map(fn(_subject) { Nil })
}

/// Initialize the model and trigger initial effects
fn init(
  config: Config,
  colors: theme.Colors,
  coord: Subject(coordinator.Msg),
) -> #(Model, Effect(Msg)) {
  let initial_model = model.init(config, colors)

  // Request initial beads load
  let effects = [
    fn() {
      coordinator.send(coord, coordinator.RefreshBeads)
      model.Tick
    },
  ]

  #(initial_model, effects)
}
