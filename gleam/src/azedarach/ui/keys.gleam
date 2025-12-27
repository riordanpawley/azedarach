// Keyboard handling - map keys to messages

import gleam/int
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/string
import azedarach/domain/session
import azedarach/domain/task
import azedarach/services/image
import azedarach/ui/model.{
  type Model, type Msg, type Overlay,
  Normal, Select,
}
import azedarach/ui/textfield

// Key event from Shore
pub type KeyEvent {
  KeyEvent(key: String, modifiers: List(Modifier))
}

pub type Modifier {
  Ctrl
  Shift
  Alt
}

// Map key event to message based on current state
pub fn handle_key(model: Model, event: KeyEvent) -> Option(Msg) {
  // Check for input mode first
  case model.input {
    Some(_) -> handle_input_key(event)
    None -> {
      // Check for overlay
      case model.overlay {
        Some(overlay) -> handle_overlay_key(overlay, event, model)
        None -> {
          // Check for pending key (goto mode)
          case model.pending_key {
            Some("g") -> handle_goto_key(event)
            _ -> handle_normal_key(event, model)
          }
        }
      }
    }
  }
}

// Input mode key handling
fn handle_input_key(event: KeyEvent) -> Option(Msg) {
  case event.key {
    "escape" -> Some(model.InputCancel)
    "enter" | "return" -> Some(model.InputSubmit)
    "backspace" -> Some(model.InputBackspace)
    key -> {
      case is_printable(key) {
        True -> Some(model.InputChar(key))
        False -> None
      }
    }
  }
}

// Overlay key handling
fn handle_overlay_key(
  overlay: Overlay,
  event: KeyEvent,
  model: Model,
) -> Option(Msg) {
  case overlay {
    model.ActionMenu -> handle_action_menu_key(event)
    model.SortMenu -> handle_sort_menu_key(event)
    model.FilterMenu -> handle_filter_menu_key(event)
    model.StatusFilterMenu -> handle_status_filter_menu_key(event, model)
    model.PriorityFilterMenu -> handle_priority_filter_menu_key(event, model)
    model.TypeFilterMenu -> handle_type_filter_menu_key(event, model)
    model.SessionFilterMenu -> handle_session_filter_menu_key(event, model)
    model.HelpOverlay -> handle_simple_close(event)
    model.SettingsOverlay(_) -> handle_settings_key(event)
    model.DiagnosticsOverlay -> handle_simple_close(event)
    model.LogsViewer -> handle_simple_close(event)
    model.ProjectSelector -> handle_project_selector_key(event)
    model.DetailPanel(_) -> handle_detail_panel_key(event)
    model.ImageAttach(_) -> handle_image_attach_key(event)
    model.ImageList(bead_id) -> handle_image_list_key(event, bead_id)
    model.ImagePreview(_) -> handle_simple_close(event)
    model.DevServerMenu(_) -> handle_simple_close(event)
    model.DiffViewer(_) -> handle_simple_close(event)
    model.MergeChoice(_, _, merge_in_progress) -> handle_merge_choice_key(event, merge_in_progress)
    model.ConfirmDialog(_) -> handle_confirm_key(event)
    model.PlanningOverlay(state) -> handle_planning_key(event, state)
  }
}

fn handle_action_menu_key(event: KeyEvent) -> Option(Msg) {
  case event.key, has_modifier(event, Shift) {
    "escape", _ -> Some(model.CloseOverlay)
    "s", False -> Some(model.StartSession)
    "s", True -> Some(model.StartSessionWithWork)
    "!", _ -> Some(model.StartSessionYolo)
    "a", _ -> Some(model.AttachSession)
    "p", False -> Some(model.PauseSession)
    "p", True -> Some(model.CreatePR)
    "r", False -> {
      case has_modifier(event, Ctrl) {
        True -> Some(model.RestartDevServer)
        False -> Some(model.ResumeSession)
      }
    }
    "x", _ -> Some(model.StopSession)
    "v", _ -> Some(model.ViewDevServer)
    "u", _ -> Some(model.UpdateFromMain)
    "m", _ -> Some(model.MergeToMain)
    "f", _ -> Some(model.ShowDiff)
    "d", _ -> Some(model.DeleteCleanup)
    "h", _ -> Some(model.MoveTaskLeft)
    "l", _ -> Some(model.MoveTaskRight)
    _, _ -> None
  }
}

fn handle_sort_menu_key(event: KeyEvent) -> Option(Msg) {
  case event.key {
    "escape" -> Some(model.CloseOverlay)
    "s" -> Some(model.SetSort(model.SortBySession))
    "p" -> Some(model.SetSort(model.SortByPriority))
    "u" -> Some(model.SetSort(model.SortByUpdated))
    _ -> None
  }
}

fn handle_filter_menu_key(event: KeyEvent) -> Option(Msg) {
  case event.key, has_modifier(event, Shift) {
    "escape", _ -> Some(model.CloseOverlay)
    "s", False -> Some(model.OpenStatusFilterMenu)
    "p", False -> Some(model.OpenPriorityFilterMenu)
    "t", False -> Some(model.OpenTypeFilterMenu)
    "s", True -> Some(model.OpenSessionFilterMenu)
    // Shift+S for Session
    "e", _ -> Some(model.ToggleHideEpicChildren)
    "c", _ -> Some(model.ClearFilters)
    _, _ -> None
  }
}

fn handle_status_filter_menu_key(event: KeyEvent, model: Model) -> Option(Msg) {
  let _ = model
  // Used for future filter state display
  case event.key {
    "escape" -> Some(model.OpenFilterMenu)
    // Back to filter menu
    "q" -> Some(model.CloseOverlay)
    "o" -> Some(model.ToggleStatusFilter(task.Open))
    "i" -> Some(model.ToggleStatusFilter(task.InProgress))
    "r" -> Some(model.ToggleStatusFilter(task.Review))
    "d" -> Some(model.ToggleStatusFilter(task.Done))
    "b" -> Some(model.ToggleStatusFilter(task.Blocked))
    _ -> None
  }
}

fn handle_priority_filter_menu_key(event: KeyEvent, model: Model) -> Option(Msg) {
  let _ = model
  // Used for future filter state display
  case event.key {
    "escape" -> Some(model.OpenFilterMenu)
    // Back to filter menu
    "q" -> Some(model.CloseOverlay)
    "1" -> Some(model.TogglePriorityFilter(task.P1))
    "2" -> Some(model.TogglePriorityFilter(task.P2))
    "3" -> Some(model.TogglePriorityFilter(task.P3))
    "4" -> Some(model.TogglePriorityFilter(task.P4))
    _ -> None
  }
}

fn handle_type_filter_menu_key(event: KeyEvent, model: Model) -> Option(Msg) {
  let _ = model
  // Used for future filter state display
  case event.key {
    "escape" -> Some(model.OpenFilterMenu)
    // Back to filter menu
    "q" -> Some(model.CloseOverlay)
    "t" -> Some(model.ToggleTypeFilter(task.TaskType))
    "b" -> Some(model.ToggleTypeFilter(task.Bug))
    "e" -> Some(model.ToggleTypeFilter(task.Epic))
    "f" -> Some(model.ToggleTypeFilter(task.Feature))
    "c" -> Some(model.ToggleTypeFilter(task.Chore))
    _ -> None
  }
}

fn handle_session_filter_menu_key(
  event: KeyEvent,
  model: Model,
) -> Option(Msg) {
  let _ = model
  // Used for future filter state display
  case event.key {
    "escape" -> Some(model.OpenFilterMenu)
    // Back to filter menu
    "q" -> Some(model.CloseOverlay)
    "i" -> Some(model.ToggleSessionFilter(session.Idle))
    "b" -> Some(model.ToggleSessionFilter(session.Busy))
    "w" -> Some(model.ToggleSessionFilter(session.Waiting))
    "d" -> Some(model.ToggleSessionFilter(session.Done))
    "e" -> Some(model.ToggleSessionFilter(session.Error))
    "p" -> Some(model.ToggleSessionFilter(session.Paused))
    _ -> None
  }
}

fn handle_settings_key(event: KeyEvent) -> Option(Msg) {
  case event.key {
    "escape" | "q" -> Some(model.CloseOverlay)
    "j" | "down" -> Some(model.SettingsNavigateDown)
    "k" | "up" -> Some(model.SettingsNavigateUp)
    " " | "enter" | "return" -> Some(model.SettingsToggleCurrent)
    _ -> None
  }
}

fn handle_simple_close(event: KeyEvent) -> Option(Msg) {
  case event.key {
    "escape" | "q" -> Some(model.CloseOverlay)
    _ -> None
  }
}

fn handle_project_selector_key(event: KeyEvent) -> Option(Msg) {
  case event.key {
    "escape" | "q" -> Some(model.CloseOverlay)
    // Numbers 1-9 select projects (0-indexed internally)
    key -> {
      case int.parse(key) {
        Ok(n) if n >= 1 && n <= 9 -> Some(model.SelectProject(n - 1))
        _ -> None
      }
    }
  }
}

fn handle_detail_panel_key(event: KeyEvent) -> Option(Msg) {
  case event.key, has_modifier(event, Shift) {
    "escape", _ | "q", _ -> Some(model.CloseOverlay)
    "e", _ -> Some(model.EditBead)
    "i", False -> Some(model.AttachImage)
    "i", True -> Some(model.OpenImageList)
    // Shift+I for image list
    // Scroll with j/k or ctrl+u/d
    _, _ -> None
  }
}

fn handle_image_attach_key(event: KeyEvent) -> Option(Msg) {
  case event.key {
    "escape" -> Some(model.CloseOverlay)
    "p" | "v" -> Some(model.PasteFromClipboard)
    "f" -> Some(model.SelectFile)
    _ -> None
  }
}

fn handle_image_list_key(event: KeyEvent, bead_id: String) -> Option(Msg) {
  let is_shift = has_modifier(event, Shift)

  case event.key {
    "escape" | "q" -> Some(model.CloseOverlay)
    "a" -> Some(model.AttachImage)
    // add new image
    // Number keys: plain = open, shift = delete
    key -> {
      case int.parse(key) {
        Ok(n) if n >= 1 && n <= 9 -> {
          // Get the nth attachment
          case image.list(bead_id) {
            Ok(attachments) -> {
              case list.drop(attachments, n - 1) |> list.first {
                Ok(attachment) -> {
                  case is_shift {
                    True -> Some(model.DeleteImage(attachment.id))
                    False -> Some(model.OpenImage(bead_id, attachment.id))
                  }
                }
                Error(_) -> None
              }
            }
            Error(_) -> None
          }
        }
        _ -> None
      }
    }
  }
}

fn handle_merge_choice_key(event: KeyEvent, merge_in_progress: Bool) -> Option(Msg) {
  case event.key, merge_in_progress {
    "escape", _ -> Some(model.CloseOverlay)
    "m", False -> Some(model.MergeAndAttach)  // Only when no merge in progress
    "s", _ -> Some(model.SkipAndAttach)
    "a", True -> Some(model.AbortMerge)  // Only when merge in progress
    _, _ -> None
  }
}

fn handle_confirm_key(event: KeyEvent) -> Option(Msg) {
  case event.key {
    "escape" | "n" -> Some(model.CancelAction)
    "y" -> Some(model.ConfirmAction)
    _ -> None
  }
}

fn handle_planning_key(
  event: KeyEvent,
  state: model.PlanningOverlayState,
) -> Option(Msg) {
  case state {
    // Input state - use TextField key handling
    model.PlanningInput(field) -> {
      let mods = textfield.Modifiers(
        ctrl: has_modifier(event, Ctrl),
        alt: has_modifier(event, Alt),
        shift: has_modifier(event, Shift),
      )
      case textfield.handle_key(field, event.key, mods) {
        textfield.Updated(new_field) ->
          Some(model.PlanningFieldUpdate(new_field))
        textfield.Submit(_) -> Some(model.PlanningSubmit)
        textfield.Cancel -> Some(model.PlanningCancel)
        textfield.Ignored -> None
      }
    }
    // Generating/Reviewing/Creating - allow cancel or attach
    model.PlanningGenerating(_)
    | model.PlanningReviewing(_, _, _)
    | model.PlanningCreatingBeads(_) -> {
      case event.key {
        "escape" -> Some(model.PlanningCancel)
        "a" -> Some(model.PlanningAttachSession)
        _ -> None
      }
    }
    // Complete or Error - close overlay
    model.PlanningComplete(_) | model.PlanningError(_) -> {
      case event.key {
        "escape" | "q" | "enter" | "return" -> Some(model.CloseOverlay)
        _ -> None
      }
    }
  }
}

// Goto mode (after pressing 'g')
fn handle_goto_key(event: KeyEvent) -> Option(Msg) {
  case event.key {
    "escape" -> Some(model.ExitGoto)
    "g" -> Some(model.GotoFirst)
    "e" -> Some(model.GotoLast)
    "h" -> Some(model.GotoColumn(0))
    "l" -> Some(model.GotoColumn(3))
    "p" -> Some(model.OpenProjectSelector)
    // 'w' for jump labels - needs additional state
    _ -> Some(model.ExitGoto)
  }
}

// Normal mode key handling
fn handle_normal_key(event: KeyEvent, model: Model) -> Option(Msg) {
  case model.mode {
    Normal -> handle_normal_mode_key(event, model)
    Select(_) -> handle_select_mode_key(event)
  }
}

fn handle_normal_mode_key(event: KeyEvent, model: Model) -> Option(Msg) {
  case event.key, has_modifier(event, Shift), has_modifier(event, Ctrl) {
    // Navigation
    "h", _, _ -> Some(model.MoveLeft)
    "left", _, _ -> Some(model.MoveLeft)
    "j", _, _ -> Some(model.MoveDown)
    "down", _, _ -> Some(model.MoveDown)
    "k", _, _ -> Some(model.MoveUp)
    "up", _, _ -> Some(model.MoveUp)
    "l", False, False -> Some(model.MoveRight)
    "right", _, _ -> Some(model.MoveRight)
    "d", _, True -> Some(model.PageDown)
    // Ctrl+d
    "u", _, True -> Some(model.PageUp)
    // Ctrl+u

    // Mode switches
    "space", _, _ -> Some(model.OpenActionMenu)
    "/", _, _ -> Some(model.EnterSearch)
    "f", _, _ -> Some(model.OpenFilterMenu)
    ",", _, _ -> Some(model.OpenSortMenu)
    "v", _, _ -> Some(model.EnterSelect)
    "g", _, _ -> Some(model.EnterGoto)

    // Quick actions - Enter on epic drills down, otherwise opens detail
    "enter", _, _ -> handle_enter_key(model)
    "return", _, _ -> handle_enter_key(model)
    // Escape exits epic drill-down mode (if active)
    "escape", _, _ -> handle_escape_key(model)
    "?", _, _ -> Some(model.OpenHelp)
    "s", False, _ -> Some(model.OpenSettings)
    "d", False, _ -> Some(model.OpenDiagnostics)
    "p", False, _ -> Some(model.OpenPlanning)
    "l", True, _ -> Some(model.OpenLogs)
    // Shift+L
    "c", False, _ -> Some(model.CreateBead)
    "c", True, _ -> Some(model.CreateBeadWithClaude)
    // Shift+C
    "r", True, _ -> Some(model.ForceRedraw)
    // Shift+R (refresh)

    // Quit (or exit drill-down if active)
    "q", _, _ -> handle_quit_key(model)

    // Redraw
    "l", _, True -> Some(model.ForceRedraw)
    // Ctrl+L

    _, _, _ -> None
  }
}

// Handle Enter key - drill down into epics, detail panel for others
fn handle_enter_key(model: Model) -> Option(Msg) {
  case get_current_task(model) {
    Some(t) -> {
      case task.is_epic(t) {
        True -> Some(model.DrillDownEpic(t.id))
        False -> Some(model.OpenDetailPanel)
      }
    }
    None -> None
  }
}

// Handle Escape key - exit epic drill-down if active, otherwise no action
fn handle_escape_key(model: Model) -> Option(Msg) {
  case model.current_epic {
    Some(_) -> Some(model.ExitEpicDrill)
    None -> None
  }
}

// Handle q key - exit drill-down if active, otherwise quit
fn handle_quit_key(model: Model) -> Option(Msg) {
  case model.current_epic {
    Some(_) -> Some(model.ExitEpicDrill)
    None -> Some(model.Quit)
  }
}

// Get the task at the current cursor position
fn get_current_task(model: Model) -> Option(task.Task) {
  let tasks = model.tasks_in_column(model, model.cursor.column_index)
  case list.drop(tasks, model.cursor.task_index) |> list.first {
    Ok(t) -> Some(t)
    Error(_) -> None
  }
}

fn handle_select_mode_key(event: KeyEvent) -> Option(Msg) {
  case event.key {
    // Navigation still works
    "h" | "left" -> Some(model.MoveLeft)
    "j" | "down" -> Some(model.MoveDown)
    "k" | "up" -> Some(model.MoveUp)
    "l" | "right" -> Some(model.MoveRight)

    // Toggle selection
    "space" -> Some(model.ToggleSelection)

    // Exit
    "escape" | "v" -> Some(model.ExitSelect)

    _ -> None
  }
}

// Helpers
fn has_modifier(event: KeyEvent, mod: Modifier) -> Bool {
  list.contains(event.modifiers, mod)
}

fn is_printable(key: String) -> Bool {
  case string.length(key) {
    1 -> True
    _ -> False
  }
}
