// TEA Update - message handling and state transitions
//
// All side effects go through Shore's effect system via the effects module.
// The update function is pure - it returns effects, not executes them.
//
// Optimistic Updates:
// For task moves, we apply the status change immediately (optimistic) and
// send the bd command async. On success, we confirm; on failure, we rollback.

import gleam/dict
import gleam/int
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/result
import gleam/set
import gleam/string
import gleam/erlang/process.{type Subject}
import optimist
import azedarach/domain/session
import azedarach/domain/task
import azedarach/ui/model.{
  type Model, type Msg, type Mode, type Overlay, type InputState,
  Cursor, Normal, Select,
}
import azedarach/ui/effects.{type Effect}
import azedarach/actors/coordinator
import azedarach/actors/app_supervisor.{type AppContext}

pub fn update(
  model: Model,
  msg: Msg,
  coord: Subject(coordinator.Msg),
) -> #(Model, Effect(Msg)) {
  case msg {
    // Navigation - pure state updates, no effects
    model.MoveUp -> #(move_cursor(model, 0, -1), effects.none())
    model.MoveDown -> #(move_cursor(model, 0, 1), effects.none())
    model.MoveLeft -> #(move_cursor(model, -1, 0), effects.none())
    model.MoveRight -> #(move_cursor(model, 1, 0), effects.none())
    model.PageUp -> #(move_cursor(model, 0, -10), effects.none())
    model.PageDown -> #(move_cursor(model, 0, 10), effects.none())
    model.GotoFirst -> #(goto_first(model), effects.none())
    model.GotoLast -> #(goto_last(model), effects.none())
    model.GotoColumn(col) -> #(goto_column(model, col), effects.none())

    // Mode changes - pure state updates
    model.EnterSelect -> #(enter_select(model), effects.none())
    model.ExitSelect -> #(Model(..model, mode: Normal), effects.none())
    model.ToggleSelection -> #(toggle_selection(model), effects.none())
    model.EnterGoto -> #(Model(..model, pending_key: Some("g")), effects.none())
    model.ExitGoto -> #(Model(..model, pending_key: None), effects.none())
    model.EnterSearch -> #(
      Model(..model, input: Some(model.SearchInput(""))),
      effects.none(),
    )
    model.ExitSearch -> #(
      Model(..model, input: None, search_query: ""),
      effects.none(),
    )

    // Overlays - pure state updates
    model.OpenActionMenu -> #(
      Model(..model, overlay: Some(model.ActionMenu)),
      effects.none(),
    )
    model.OpenFilterMenu -> #(
      Model(..model, overlay: Some(model.FilterMenu)),
      effects.none(),
    )
    model.OpenSortMenu -> #(
      Model(..model, overlay: Some(model.SortMenu)),
      effects.none(),
    )
    model.OpenHelp -> #(
      Model(..model, overlay: Some(model.HelpOverlay)),
      effects.none(),
    )
    model.OpenSettings -> #(
      Model(..model, overlay: Some(model.SettingsOverlay)),
      effects.none(),
    )
    model.OpenDiagnostics -> #(
      Model(..model, overlay: Some(model.DiagnosticsOverlay)),
      effects.none(),
    )
    model.OpenLogs -> #(
      Model(..model, overlay: Some(model.LogsViewer)),
      effects.none(),
    )
    model.OpenProjectSelector -> #(
      Model(..model, overlay: Some(model.ProjectSelector)),
      effects.none(),
    )
    model.OpenDetailPanel -> #(open_detail_panel(model), effects.none())
    model.CloseOverlay -> #(Model(..model, overlay: None), effects.none())

    // Session actions - side effects go through Shore effects
    model.StartSession -> {
      case current_task_id(model) {
        Some(id) -> #(model, effects.start_session(coord, id, False, False))
        None -> #(model, effects.none())
      }
    }
    model.StartSessionWithWork -> {
      case current_task_id(model) {
        Some(id) -> #(model, effects.start_session(coord, id, True, False))
        None -> #(model, effects.none())
      }
    }
    model.StartSessionYolo -> {
      case current_task_id(model) {
        Some(id) -> #(model, effects.start_session(coord, id, True, True))
        None -> #(model, effects.none())
      }
    }
    model.AttachSession -> {
      case current_task_id(model) {
        Some(id) -> #(model, effects.attach_session(coord, id))
        None -> #(model, effects.none())
      }
    }
    model.PauseSession -> {
      case current_task_id(model) {
        Some(id) -> #(model, effects.pause_session(coord, id))
        None -> #(model, effects.none())
      }
    }
    model.ResumeSession -> {
      case current_task_id(model) {
        Some(id) -> #(model, effects.resume_session(coord, id))
        None -> #(model, effects.none())
      }
    }
    model.StopSession -> {
      case current_task_id(model) {
        Some(id) -> #(
          Model(..model, overlay: Some(model.ConfirmDialog(model.StopSession(id)))),
          effects.none(),
        )
        None -> #(model, effects.none())
      }
    }

    // Dev server actions - side effects go through Shore effects
    model.ToggleDevServer -> {
      case current_task_id(model) {
        Some(id) -> #(model, effects.toggle_dev_server(coord, id, "default"))
        None -> #(model, effects.none())
      }
    }
    model.ViewDevServer -> {
      case current_task_id(model) {
        Some(id) -> #(model, effects.view_dev_server(coord, id, "default"))
        None -> #(model, effects.none())
      }
    }
    model.RestartDevServer -> {
      case current_task_id(model) {
        Some(id) -> #(model, effects.restart_dev_server(coord, id, "default"))
        None -> #(model, effects.none())
      }
    }

    // Git actions - side effects go through Shore effects
    model.UpdateFromMain -> {
      case current_task_id(model) {
        Some(id) -> #(model, effects.update_from_main(coord, id))
        None -> #(model, effects.none())
      }
    }
    model.MergeToMain -> {
      case current_task_id(model) {
        Some(id) -> #(model, effects.merge_to_main(coord, id))
        None -> #(model, effects.none())
      }
    }
    model.ShowDiff -> {
      case current_task_id(model) {
        Some(id) -> #(
          Model(..model, overlay: Some(model.DiffViewer(id))),
          effects.none(),
        )
        None -> #(model, effects.none())
      }
    }
    model.CreatePR -> {
      case current_task_id(model) {
        Some(id) -> #(model, effects.create_pr(coord, id))
        None -> #(model, effects.none())
      }
    }
    model.DeleteCleanup -> {
      case current_task_id(model) {
        Some(id) -> #(
          Model(..model, overlay: Some(model.ConfirmDialog(model.DeleteWorktree(id)))),
          effects.none(),
        )
        None -> #(model, effects.none())
      }
    }

    // Task actions - optimistic updates with async bd command
    model.MoveTaskLeft -> {
      case current_task_id(model) {
        Some(id) -> apply_optimistic_move(model, coord, id, -1)
        None -> #(model, effects.none())
      }
    }
    model.MoveTaskRight -> {
      case current_task_id(model) {
        Some(id) -> apply_optimistic_move(model, coord, id, 1)
        None -> #(model, effects.none())
      }
    }

    // Bead CRUD - side effects go through Shore effects
    model.CreateBead -> #(model, effects.create_bead(coord))
    model.CreateBeadWithClaude -> #(model, effects.create_bead_with_claude(coord))
    model.EditBead -> {
      case current_task_id(model) {
        Some(id) -> #(model, effects.edit_bead(coord, id))
        None -> #(model, effects.none())
      }
    }
    model.DeleteBead -> {
      case current_task_id(model) {
        Some(id) -> #(
          Model(..model, overlay: Some(model.ConfirmDialog(model.DeleteBead(id)))),
          effects.none(),
        )
        None -> #(model, effects.none())
      }
    }

    // Image actions - side effects go through Shore effects
    model.AttachImage -> {
      case current_task_id(model) {
        Some(id) -> #(
          Model(..model, overlay: Some(model.ImageAttach(id))),
          effects.none(),
        )
        None -> #(model, effects.none())
      }
    }
    model.PasteFromClipboard -> {
      case current_task_id(model) {
        Some(id) -> #(
          Model(..model, overlay: None),
          effects.paste_image(coord, id),
        )
        None -> #(model, effects.none())
      }
    }
    model.SelectFile -> #(
      Model(..model, input: Some(model.PathInput(""))),
      effects.none(),
    )
    model.PreviewImage(path) -> #(
      Model(..model, overlay: Some(model.ImagePreview(path))),
      effects.none(),
    )
    model.DeleteImage(path) -> {
      case current_task_id(model) {
        Some(id) -> #(model, effects.delete_image(coord, id, path))
        None -> #(model, effects.none())
      }
    }

    // Input handling - pure state updates
    model.InputChar(c) -> #(handle_input_char(model, c), effects.none())
    model.InputBackspace -> #(handle_input_backspace(model), effects.none())
    model.InputSubmit -> #(handle_input_submit(model), effects.none())
    model.InputCancel -> #(Model(..model, input: None), effects.none())

    // Filter/sort - pure state updates
    model.ToggleStatusFilter(status) -> #(toggle_status_filter(model, status), effects.none())
    model.TogglePriorityFilter(priority) -> #(toggle_priority_filter(model, priority), effects.none())
    model.ToggleTypeFilter(t) -> #(toggle_type_filter(model, t), effects.none())
    model.ToggleSessionFilter(state) -> #(toggle_session_filter(model, state), effects.none())
    model.ToggleHideEpicChildren -> #(
      Model(..model, hide_epic_children: !model.hide_epic_children),
      effects.none(),
    )
    model.ClearFilters -> #(clear_filters(model), effects.none())
    model.SetSort(field) -> #(
      Model(..model, sort_by: field, overlay: None),
      effects.none(),
    )

    // MergeChoice - side effects go through Shore effects
    model.MergeAndAttach -> {
      case model.overlay {
        Some(model.MergeChoice(id, _)) -> #(
          Model(..model, overlay: None),
          effects.merge_and_attach(coord, id),
        )
        _ -> #(model, effects.none())
      }
    }
    model.SkipAndAttach -> {
      case model.overlay {
        Some(model.MergeChoice(id, _)) -> #(
          Model(..model, overlay: None),
          effects.attach_session(coord, id),
        )
        _ -> #(model, effects.none())
      }
    }

    // Confirm dialog - uses handle_confirm which returns effects
    model.ConfirmAction -> handle_confirm(model, coord)
    model.CancelAction -> #(Model(..model, overlay: None), effects.none())

    // Data updates - pure state updates (from coordinator async messages)
    model.BeadsLoaded(tasks) -> {
      // Reconcile optimistic updates with actual task statuses
      let reconciled = reconcile_optimistic_updates(model.optimistic_statuses, tasks)
      #(
        Model(..model, tasks: tasks, optimistic_statuses: reconciled, loading: False),
        effects.none(),
      )
    }
    model.SessionStateChanged(id, state) -> #(
      Model(..model, sessions: dict.insert(model.sessions, id, state)),
      effects.none(),
    )
    model.DevServerStateChanged(id, state) -> #(
      Model(..model, dev_servers: dict.insert(model.dev_servers, id, state)),
      effects.none(),
    )
    model.ToastExpired(id) -> #(
      Model(..model, toasts: list.filter(model.toasts, fn(t) { t.expires_at != id })),
      effects.none(),
    )

    // Optimistic update responses
    model.TaskMoveSucceeded(id, _new_status) -> {
      // Confirm the optimistic update - remove from pending
      let new_optimistic = dict.delete(model.optimistic_statuses, id)
      #(Model(..model, optimistic_statuses: new_optimistic), effects.none())
    }
    model.TaskMoveFailed(id, error) -> {
      // Rollback the optimistic update and show error
      let new_optimistic = case dict.get(model.optimistic_statuses, id) {
        Ok(opt) -> {
          // Revert to original state, then remove from pending
          let _reverted = optimist.revert(opt)
          dict.delete(model.optimistic_statuses, id)
        }
        Error(_) -> model.optimistic_statuses
      }
      let toast = model.Toast(
        message: "Move failed: " <> error,
        level: model.Error,
        expires_at: 0,  // Will be set by toast system
      )
      #(
        Model(
          ..model,
          optimistic_statuses: new_optimistic,
          toasts: [toast, ..model.toasts],
        ),
        effects.none(),
      )
    }

    // System messages - pure state updates
    model.TerminalResized(w, h) -> #(
      Model(..model, terminal_size: #(w, h)),
      effects.none(),
    )
    model.Tick -> #(model, effects.none())
    model.Quit -> #(model, effects.none())
    // Will be handled by Shore
    model.ForceRedraw -> #(model, effects.none())
    model.KeyPressed(_, _) -> #(model, effects.none())
    // Handled by keys module
  }
}

/// Update with supervision context (preferred method)
/// This version can start/stop session monitors through the supervision tree
pub fn update_with_context(
  model: Model,
  msg: Msg,
  context: AppContext,
) -> #(Model, Effect(Msg)) {
  // Use the context's coordinator
  let #(new_model, effects) = update(model, msg, context.coordinator)

  // Handle supervision-related side effects
  case msg {
    // When a session is started, start the session monitor
    model.SessionStateChanged(id, state) -> {
      case state.tmux_session, state.state {
        Some(tmux_name), session.Busy -> {
          app_supervisor.start_session_monitor(context, id, tmux_name)
          #(new_model, effects)
        }
        _, session.Idle -> {
          app_supervisor.stop_session_monitor(context, id)
          #(new_model, effects)
        }
        _, _ -> #(new_model, effects)
      }
    }
    // When a dev server is started/stopped, manage server monitor
    model.DevServerStateChanged(_key, _state) -> {
      // Server monitors are managed by the servers supervisor
      #(new_model, effects)
    }
    _ -> #(new_model, effects)
  }
}

// Helper functions

fn move_cursor(model: Model, dx: Int, dy: Int) -> Model {
  let new_col = int.clamp(model.cursor.column_index + dx, 0, 3)
  let tasks_in_col = model.tasks_in_column(model, new_col)
  let max_idx = int.max(0, list.length(tasks_in_col) - 1)
  let new_idx = int.clamp(model.cursor.task_index + dy, 0, max_idx)

  Model(..model, cursor: Cursor(column_index: new_col, task_index: new_idx))
}

fn goto_first(model: Model) -> Model {
  Model(..model, cursor: Cursor(..model.cursor, task_index: 0))
}

fn goto_last(model: Model) -> Model {
  let tasks = model.tasks_in_column(model, model.cursor.column_index)
  let last_idx = int.max(0, list.length(tasks) - 1)
  Model(..model, cursor: Cursor(..model.cursor, task_index: last_idx))
}

fn goto_column(model: Model, col: Int) -> Model {
  let clamped = int.clamp(col, 0, 3)
  Model(..model, cursor: Cursor(column_index: clamped, task_index: 0))
}

fn enter_select(model: Model) -> Model {
  case current_task_id(model) {
    Some(id) -> Model(..model, mode: Select(set.from_list([id])))
    None -> Model(..model, mode: Select(set.new()))
  }
}

fn toggle_selection(model: Model) -> Model {
  case model.mode, current_task_id(model) {
    Select(selected), Some(id) -> {
      let new_selected = case set.contains(selected, id) {
        True -> set.delete(selected, id)
        False -> set.insert(selected, id)
      }
      Model(..model, mode: Select(new_selected))
    }
    _, _ -> model
  }
}

fn open_detail_panel(model: Model) -> Model {
  case current_task_id(model) {
    Some(id) -> Model(..model, overlay: Some(model.DetailPanel(id)))
    None -> model
  }
}

fn current_task_id(model: Model) -> Option(String) {
  let tasks = model.tasks_in_column(model, model.cursor.column_index)
  case list.at(tasks, model.cursor.task_index) {
    Ok(task) -> Some(task.id)
    Error(_) -> None
  }
}

fn handle_input_char(model: Model, c: String) -> Model {
  case model.input {
    Some(model.SearchInput(q)) ->
      Model(..model, input: Some(model.SearchInput(q <> c)))
    Some(model.TitleInput(t)) ->
      Model(..model, input: Some(model.TitleInput(t <> c)))
    Some(model.NotesInput(n)) ->
      Model(..model, input: Some(model.NotesInput(n <> c)))
    Some(model.PathInput(p)) ->
      Model(..model, input: Some(model.PathInput(p <> c)))
    None -> model
  }
}

fn handle_input_backspace(model: Model) -> Model {
  case model.input {
    Some(model.SearchInput(q)) ->
      Model(..model, input: Some(model.SearchInput(string.drop_end(q, 1))))
    Some(model.TitleInput(t)) ->
      Model(..model, input: Some(model.TitleInput(string.drop_end(t, 1))))
    Some(model.NotesInput(n)) ->
      Model(..model, input: Some(model.NotesInput(string.drop_end(n, 1))))
    Some(model.PathInput(p)) ->
      Model(..model, input: Some(model.PathInput(string.drop_end(p, 1))))
    None -> model
  }
}

fn handle_input_submit(model: Model) -> Model {
  case model.input {
    Some(model.SearchInput(q)) ->
      Model(..model, input: None, search_query: q)
    _ -> Model(..model, input: None)
  }
}

fn toggle_status_filter(model: Model, status: task.Status) -> Model {
  let new_filter = case set.contains(model.status_filter, status) {
    True -> set.delete(model.status_filter, status)
    False -> set.insert(model.status_filter, status)
  }
  Model(..model, status_filter: new_filter)
}

fn toggle_priority_filter(model: Model, priority: task.Priority) -> Model {
  let new_filter = case set.contains(model.priority_filter, priority) {
    True -> set.delete(model.priority_filter, priority)
    False -> set.insert(model.priority_filter, priority)
  }
  Model(..model, priority_filter: new_filter)
}

fn toggle_type_filter(model: Model, t: task.IssueType) -> Model {
  let new_filter = case set.contains(model.type_filter, t) {
    True -> set.delete(model.type_filter, t)
    False -> set.insert(model.type_filter, t)
  }
  Model(..model, type_filter: new_filter)
}

fn toggle_session_filter(model: Model, state: session.State) -> Model {
  let new_filter = case set.contains(model.session_filter, state) {
    True -> set.delete(model.session_filter, state)
    False -> set.insert(model.session_filter, state)
  }
  Model(..model, session_filter: new_filter)
}

fn clear_filters(model: Model) -> Model {
  Model(
    ..model,
    status_filter: set.new(),
    priority_filter: set.new(),
    type_filter: set.new(),
    session_filter: set.new(),
    hide_epic_children: False,
    search_query: "",
    overlay: None,
  )
}

fn handle_confirm(
  model: Model,
  coord: Subject(coordinator.Msg),
) -> #(Model, Effect(Msg)) {
  case model.overlay {
    Some(model.ConfirmDialog(action)) -> {
      let effect = case action {
        model.DeleteWorktree(id) -> effects.delete_cleanup(coord, id)
        model.DeleteBead(id) -> effects.delete_bead(coord, id)
        model.StopSession(id) -> effects.stop_session(coord, id)
      }
      #(Model(..model, overlay: None), effect)
    }
    _ -> #(model, effects.none())
  }
}

// =============================================================================
// Optimistic Update Helpers
// =============================================================================

/// Apply an optimistic move - update UI immediately, send async command
fn apply_optimistic_move(
  model: Model,
  coord: Subject(coordinator.Msg),
  id: String,
  direction: Int,
) -> #(Model, Effect(Msg)) {
  // Find the current task to get its current status
  case find_task(model, id) {
    Some(found_task) -> {
      let current_status = model.get_effective_status(found_task, model.optimistic_statuses)
      let new_status = next_status(current_status, direction)

      // Create or update optimistic state
      let opt = case dict.get(model.optimistic_statuses, id) {
        Ok(existing) -> optimist.push(existing, new_status)
        Error(_) -> optimist.from(current_status) |> optimist.push(new_status)
      }

      let new_optimistic = dict.insert(model.optimistic_statuses, id, opt)
      let new_model = Model(..model, optimistic_statuses: new_optimistic, overlay: None)

      // Send the async command to coordinator
      #(new_model, effects.move_task(coord, id, direction))
    }
    None -> #(model, effects.none())
  }
}

/// Find a task by ID in the model
fn find_task(model: Model, id: String) -> Option(task.Task) {
  list.find(model.tasks, fn(t) { t.id == id })
  |> result.map(fn(t) { Some(t) })
  |> result.unwrap(None)
}

/// Calculate next status based on direction
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

/// Reconcile optimistic updates with actual task statuses
/// If actual status matches optimistic status, the update was applied - remove from pending
/// Otherwise, keep the optimistic update (command might still be in progress or failed)
fn reconcile_optimistic_updates(
  optimistic: dict.Dict(String, optimist.Optimistic(task.Status)),
  tasks: List(task.Task),
) -> dict.Dict(String, optimist.Optimistic(task.Status)) {
  dict.fold(optimistic, dict.new(), fn(acc, id, opt) {
    case list.find(tasks, fn(t) { t.id == id }) {
      Ok(found_task) -> {
        let optimistic_status = optimist.unwrap(opt)
        case found_task.status == optimistic_status {
          // Actual status matches optimistic - update was applied, remove from pending
          True -> acc
          // Status doesn't match - keep optimistic (command in flight or will be handled by failure message)
          False -> dict.insert(acc, id, opt)
        }
      }
      // Task not found - remove the optimistic update
      Error(_) -> acc
    }
  })
}
