// TEA Update - message handling and state transitions

import gleam/dict
import gleam/int
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/set
import gleam/string
import gleam/erlang/process.{type Subject}
import azedarach/domain/session
import azedarach/domain/task
import azedarach/ui/model.{
  type Model, type Msg, type Mode, type Overlay, type InputState,
  Cursor, Normal, Select,
}
import azedarach/ui/app.{type Cmd}
import azedarach/actors/coordinator

pub fn update(
  model: Model,
  msg: Msg,
  coord: Subject(coordinator.Msg),
) -> #(Model, Cmd) {
  case msg {
    // Navigation
    model.MoveUp -> #(move_cursor(model, 0, -1), app.None)
    model.MoveDown -> #(move_cursor(model, 0, 1), app.None)
    model.MoveLeft -> #(move_cursor(model, -1, 0), app.None)
    model.MoveRight -> #(move_cursor(model, 1, 0), app.None)
    model.PageUp -> #(move_cursor(model, 0, -10), app.None)
    model.PageDown -> #(move_cursor(model, 0, 10), app.None)
    model.GotoFirst -> #(goto_first(model), app.None)
    model.GotoLast -> #(goto_last(model), app.None)
    model.GotoColumn(col) -> #(goto_column(model, col), app.None)

    // Mode changes
    model.EnterSelect -> #(enter_select(model), app.None)
    model.ExitSelect -> #(Model(..model, mode: Normal), app.None)
    model.ToggleSelection -> #(toggle_selection(model), app.None)
    model.EnterGoto -> #(Model(..model, pending_key: Some("g")), app.None)
    model.ExitGoto -> #(Model(..model, pending_key: None), app.None)
    model.EnterSearch -> #(
      Model(..model, input: Some(model.SearchInput(""))),
      app.None,
    )
    model.ExitSearch -> #(
      Model(..model, input: None, search_query: ""),
      app.None,
    )

    // Overlays
    model.OpenActionMenu -> #(
      Model(..model, overlay: Some(model.ActionMenu)),
      app.None,
    )
    model.OpenFilterMenu -> #(
      Model(..model, overlay: Some(model.FilterMenu)),
      app.None,
    )
    model.OpenSortMenu -> #(
      Model(..model, overlay: Some(model.SortMenu)),
      app.None,
    )
    model.OpenHelp -> #(
      Model(..model, overlay: Some(model.HelpOverlay)),
      app.None,
    )
    model.OpenSettings -> #(
      Model(..model, overlay: Some(model.SettingsOverlay)),
      app.None,
    )
    model.OpenDiagnostics -> #(
      Model(..model, overlay: Some(model.DiagnosticsOverlay)),
      app.None,
    )
    model.OpenLogs -> #(
      Model(..model, overlay: Some(model.LogsViewer)),
      app.None,
    )
    model.OpenProjectSelector -> #(
      Model(..model, overlay: Some(model.ProjectSelector)),
      app.None,
    )
    model.OpenDetailPanel -> #(open_detail_panel(model), app.None)
    model.CloseOverlay -> #(Model(..model, overlay: None), app.None)

    // Session actions
    model.StartSession -> {
      case current_task_id(model) {
        Some(id) -> {
          coordinator.send(coord, coordinator.StartSession(id, False, False))
          #(model, app.None)
        }
        None -> #(model, app.None)
      }
    }
    model.StartSessionWithWork -> {
      case current_task_id(model) {
        Some(id) -> {
          coordinator.send(coord, coordinator.StartSession(id, True, False))
          #(model, app.None)
        }
        None -> #(model, app.None)
      }
    }
    model.StartSessionYolo -> {
      case current_task_id(model) {
        Some(id) -> {
          coordinator.send(coord, coordinator.StartSession(id, True, True))
          #(model, app.None)
        }
        None -> #(model, app.None)
      }
    }
    model.AttachSession -> {
      case current_task_id(model) {
        Some(id) -> {
          coordinator.send(coord, coordinator.AttachSession(id))
          #(model, app.None)
        }
        None -> #(model, app.None)
      }
    }
    model.PauseSession -> {
      case current_task_id(model) {
        Some(id) -> {
          coordinator.send(coord, coordinator.PauseSession(id))
          #(model, app.None)
        }
        None -> #(model, app.None)
      }
    }
    model.ResumeSession -> {
      case current_task_id(model) {
        Some(id) -> {
          coordinator.send(coord, coordinator.ResumeSession(id))
          #(model, app.None)
        }
        None -> #(model, app.None)
      }
    }
    model.StopSession -> {
      case current_task_id(model) {
        Some(id) -> #(
          Model(..model, overlay: Some(model.ConfirmDialog(model.StopSession(id)))),
          app.None,
        )
        None -> #(model, app.None)
      }
    }

    // Dev server actions
    model.ToggleDevServer -> {
      case current_task_id(model) {
        Some(id) -> {
          coordinator.send(coord, coordinator.ToggleDevServer(id, "default"))
          #(model, app.None)
        }
        None -> #(model, app.None)
      }
    }
    model.ViewDevServer -> {
      case current_task_id(model) {
        Some(id) -> {
          coordinator.send(coord, coordinator.ViewDevServer(id, "default"))
          #(model, app.None)
        }
        None -> #(model, app.None)
      }
    }
    model.RestartDevServer -> {
      case current_task_id(model) {
        Some(id) -> {
          coordinator.send(coord, coordinator.RestartDevServer(id, "default"))
          #(model, app.None)
        }
        None -> #(model, app.None)
      }
    }

    // Git actions
    model.UpdateFromMain -> {
      case current_task_id(model) {
        Some(id) -> {
          coordinator.send(coord, coordinator.UpdateFromMain(id))
          #(model, app.None)
        }
        None -> #(model, app.None)
      }
    }
    model.MergeToMain -> {
      case current_task_id(model) {
        Some(id) -> {
          coordinator.send(coord, coordinator.MergeToMain(id))
          #(model, app.None)
        }
        None -> #(model, app.None)
      }
    }
    model.ShowDiff -> {
      case current_task_id(model) {
        Some(id) -> #(
          Model(..model, overlay: Some(model.DiffViewer(id))),
          app.None,
        )
        None -> #(model, app.None)
      }
    }
    model.CreatePR -> {
      case current_task_id(model) {
        Some(id) -> {
          coordinator.send(coord, coordinator.CreatePR(id))
          #(model, app.None)
        }
        None -> #(model, app.None)
      }
    }
    model.CompleteSession -> {
      case current_task_id(model) {
        Some(id) -> {
          // CompleteSession does the right thing based on workflow_mode
          coordinator.send(coord, coordinator.CompleteSession(id))
          #(Model(..model, overlay: None), app.None)
        }
        None -> #(model, app.None)
      }
    }
    model.DeleteCleanup -> {
      case current_task_id(model) {
        Some(id) -> #(
          Model(..model, overlay: Some(model.ConfirmDialog(model.DeleteWorktree(id)))),
          app.None,
        )
        None -> #(model, app.None)
      }
    }

    // Task actions
    model.MoveTaskLeft -> {
      case current_task_id(model) {
        Some(id) -> {
          coordinator.send(coord, coordinator.MoveTask(id, -1))
          #(Model(..model, overlay: None), app.None)
        }
        None -> #(model, app.None)
      }
    }
    model.MoveTaskRight -> {
      case current_task_id(model) {
        Some(id) -> {
          coordinator.send(coord, coordinator.MoveTask(id, 1))
          #(Model(..model, overlay: None), app.None)
        }
        None -> #(model, app.None)
      }
    }

    // Bead CRUD
    model.CreateBead -> {
      coordinator.send(coord, coordinator.CreateBead(None))
      #(model, app.None)
    }
    model.CreateBeadWithClaude -> {
      coordinator.send(coord, coordinator.CreateBeadWithClaude)
      #(model, app.None)
    }
    model.EditBead -> {
      case current_task_id(model) {
        Some(id) -> {
          coordinator.send(coord, coordinator.EditBead(id))
          #(model, app.None)
        }
        None -> #(model, app.None)
      }
    }
    model.DeleteBead -> {
      case current_task_id(model) {
        Some(id) -> #(
          Model(..model, overlay: Some(model.ConfirmDialog(model.DeleteBead(id)))),
          app.None,
        )
        None -> #(model, app.None)
      }
    }

    // Image actions
    model.AttachImage -> {
      case current_task_id(model) {
        Some(id) -> #(
          Model(..model, overlay: Some(model.ImageAttach(id))),
          app.None,
        )
        None -> #(model, app.None)
      }
    }
    model.PasteFromClipboard -> {
      case current_task_id(model) {
        Some(id) -> {
          coordinator.send(coord, coordinator.PasteImage(id))
          #(Model(..model, overlay: None), app.None)
        }
        None -> #(model, app.None)
      }
    }
    model.SelectFile -> #(
      Model(..model, input: Some(model.PathInput(""))),
      app.None,
    )
    model.PreviewImage(path) -> #(
      Model(..model, overlay: Some(model.ImagePreview(path))),
      app.None,
    )
    model.DeleteImage(path) -> {
      case current_task_id(model) {
        Some(id) -> {
          coordinator.send(coord, coordinator.DeleteImage(id, path))
          #(model, app.None)
        }
        None -> #(model, app.None)
      }
    }

    // Input handling
    model.InputChar(c) -> #(handle_input_char(model, c), app.None)
    model.InputBackspace -> #(handle_input_backspace(model), app.None)
    model.InputSubmit -> #(handle_input_submit(model), app.None)
    model.InputCancel -> #(Model(..model, input: None), app.None)

    // Filter/sort
    model.ToggleStatusFilter(status) -> #(toggle_status_filter(model, status), app.None)
    model.TogglePriorityFilter(priority) -> #(toggle_priority_filter(model, priority), app.None)
    model.ToggleTypeFilter(t) -> #(toggle_type_filter(model, t), app.None)
    model.ToggleSessionFilter(state) -> #(toggle_session_filter(model, state), app.None)
    model.ToggleHideEpicChildren -> #(
      Model(..model, hide_epic_children: !model.hide_epic_children),
      app.None,
    )
    model.ClearFilters -> #(clear_filters(model), app.None)
    model.SetSort(field) -> #(
      Model(..model, sort_by: field, overlay: None),
      app.None,
    )

    // MergeChoice
    model.MergeAndAttach -> {
      case model.overlay {
        Some(model.MergeChoice(id, _)) -> {
          coordinator.send(coord, coordinator.MergeAndAttach(id))
          #(Model(..model, overlay: None), app.None)
        }
        _ -> #(model, app.None)
      }
    }
    model.SkipAndAttach -> {
      case model.overlay {
        Some(model.MergeChoice(id, _)) -> {
          coordinator.send(coord, coordinator.AttachSession(id))
          #(Model(..model, overlay: None), app.None)
        }
        _ -> #(model, app.None)
      }
    }

    // Confirm dialog
    model.ConfirmAction -> #(handle_confirm(model, coord), app.None)
    model.CancelAction -> #(Model(..model, overlay: None), app.None)

    // Data updates
    model.BeadsLoaded(tasks) -> #(
      Model(..model, tasks: tasks, loading: False),
      app.None,
    )
    model.SessionStateChanged(id, state) -> #(
      Model(..model, sessions: dict.insert(model.sessions, id, state)),
      app.None,
    )
    model.DevServerStateChanged(id, state) -> #(
      Model(..model, dev_servers: dict.insert(model.dev_servers, id, state)),
      app.None,
    )
    model.ToastExpired(id) -> #(
      Model(..model, toasts: list.filter(model.toasts, fn(t) { t.expires_at != id })),
      app.None,
    )

    // System
    model.TerminalResized(w, h) -> #(
      Model(..model, terminal_size: #(w, h)),
      app.None,
    )
    model.Tick -> #(model, app.None)
    model.Quit -> #(model, app.None)
    // Will be handled by Shore
    model.ForceRedraw -> #(model, app.None)
    model.KeyPressed(_, _) -> #(model, app.None)
    // Handled by keys module
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

fn handle_confirm(model: Model, coord: Subject(coordinator.Msg)) -> Model {
  case model.overlay {
    Some(model.ConfirmDialog(action)) -> {
      case action {
        model.DeleteWorktree(id) ->
          coordinator.send(coord, coordinator.DeleteCleanup(id))
        model.DeleteBead(id) ->
          coordinator.send(coord, coordinator.DeleteBead(id))
        model.StopSession(id) ->
          coordinator.send(coord, coordinator.StopSession(id))
      }
      Model(..model, overlay: None)
    }
    _ -> model
  }
}
