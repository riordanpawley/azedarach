// Shore TUI Application
// Main application setup and lifecycle
//
// All side effects go through Shore's effect system.
// See effects.gleam for effect helpers.
//
// Optimistic Updates:
// The app subscribes to coordinator messages for async feedback.
// Task moves are optimistic - UI updates immediately, bd command runs async.
// Success/failure messages are polled and translated to model.Msg.

import gleam/erlang/process.{type Subject}
import gleam/result
import shore
import azedarach/config.{type Config}
import azedarach/ui/model.{type Model, type Msg}
import azedarach/ui/update
import azedarach/ui/view
import azedarach/ui/theme
import azedarach/ui/effects.{type Effect}
import azedarach/actors/coordinator
import azedarach/actors/app_supervisor.{type AppContext}

/// Start with existing supervision context (preferred method)
pub fn start_with_context(context: AppContext) -> Result(Nil, shore.StartError) {
  // Initialize theme
  let colors = theme.load(context.config.theme)

  // Create subscription subject for coordinator messages
  // This enables optimistic updates with async confirmation/rollback
  let ui_subscription = process.new_subject()
  coordinator.send(context.coordinator, coordinator.Subscribe(ui_subscription))

  // Create exit subject for graceful shutdown
  let exit = process.new_subject()

  // Configure and start Shore with supervision context
  let spec =
    shore.spec(
      init: fn() {
        init_with_context_and_subscription(
          context.config,
          colors,
          context,
          ui_subscription,
        )
      },
      view: view.render,
      update: fn(model, msg) {
        update_with_polling(model, msg, context, ui_subscription)
      },
      exit: exit,
      keybinds: shore.default_keybinds(),
      redraw: shore.on_timer(16),
    )

  // Start the application
  shore.start(spec)
  |> result.map(fn(_subject) { Nil })
}

/// Legacy start function (starts its own coordinator)
/// Deprecated: Use start_with_context instead
pub fn start(config: Config) -> Result(Nil, shore.StartError) {
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

  // Start the application
  shore.start(spec)
  |> result.map(fn(_subject) { Nil })
}

/// Initialize the model with supervision context
fn init_with_context(
  config: Config,
  colors: theme.Colors,
  context: AppContext,
) -> #(Model, Effect(Msg)) {
  let initial_model = model.init_with_context(config, colors, context)

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

/// Initialize the model with supervision context and subscription
fn init_with_context_and_subscription(
  config: Config,
  colors: theme.Colors,
  context: AppContext,
  subscription: Subject(coordinator.UiMsg),
) -> #(Model, Effect(Msg)) {
  let initial_model =
    model.init_with_context_and_subscription(config, colors, context, subscription)

  // Request initial beads load via Shore effects
  #(initial_model, effects.refresh_beads(context.coordinator))
}

/// Update with polling for coordinator messages
/// This wraps the normal update and adds an effect to poll for async messages
fn update_with_polling(
  m: Model,
  msg: Msg,
  context: AppContext,
  _subscription: Subject(coordinator.UiMsg),
) -> #(Model, Effect(Msg)) {
  // Run the normal update
  let #(new_model, base_effects) = update.update_with_context(m, msg, context)

  // On Tick, also poll for coordinator messages
  // The subscription is stored in the model, so we use that
  case msg {
    model.Tick -> {
      let poll_effect = effects.poll_coordinator_messages(new_model.ui_subscription)
      #(new_model, effects.batch([base_effects, poll_effect]))
    }
    _ -> #(new_model, base_effects)
  }
}
