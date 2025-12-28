// Keybind nodes for Shore integration
//
// This module generates KeyBind nodes for the view based on the current model state.
// Shore's design uses KeyBind nodes in the view to handle keyboard shortcuts,
// rather than a separate key handler in the update function.

import gleam/int
import gleam/list
import gleam/option.{None, Some}
import shore
import shore/key
import shore/ui
import azedarach/ui/model.{type Model, type Msg, Normal, Select}
import azedarach/util/logger

/// Node type alias for Shore nodes with our Msg type
pub type Node =
  shore.Node(Msg)

/// Generate all keybind nodes based on current model state
pub fn generate(model: Model) -> List(Node) {
  // Check for active input first - only handle escape/enter
  let result = case model.input {
    Some(_) -> {
      logger.debug("keybinds: input mode active")
      input_keybinds()
    }
    None -> {
      // Check for overlay
      case model.overlay {
        Some(overlay) -> {
          logger.debug("keybinds: overlay mode")
          overlay_keybinds(overlay, model)
        }
        None -> {
          // Check for pending key (goto mode)
          case model.pending_key {
            Some("g") -> {
              logger.debug("keybinds: goto mode")
              goto_keybinds()
            }
            _ -> {
              logger.debug("keybinds: normal mode")
              normal_keybinds(model)
            }
          }
        }
      }
    }
  }
  logger.debug("keybinds: generated " <> int.to_string(list.length(result)) <> " keybinds")
  result
}

// =============================================================================
// Input Mode Keybinds
// =============================================================================

fn input_keybinds() -> List(Node) {
  [
    ui.keybind(key.Esc, model.InputCancel),
    ui.keybind(key.Enter, model.InputSubmit),
    ui.keybind(key.Backspace, model.InputBackspace),
  ]
}

// =============================================================================
// Overlay Keybinds
// =============================================================================

fn overlay_keybinds(overlay: model.Overlay, model: Model) -> List(Node) {
  case overlay {
    model.ActionMenu -> action_menu_keybinds()
    model.SortMenu -> sort_menu_keybinds()
    model.FilterMenu -> filter_menu_keybinds()
    model.StatusFilterMenu -> status_filter_keybinds()
    model.PriorityFilterMenu -> priority_filter_keybinds()
    model.TypeFilterMenu -> type_filter_keybinds()
    model.SessionFilterMenu -> session_filter_keybinds()
    model.HelpOverlay -> simple_close_keybinds()
    model.SettingsOverlay(_) -> settings_keybinds()
    model.DiagnosticsOverlay -> simple_close_keybinds()
    model.LogsViewer -> simple_close_keybinds()
    model.ProjectSelector -> project_selector_keybinds()
    model.DetailPanel(_) -> detail_panel_keybinds()
    model.ImageAttach(_) -> image_attach_keybinds()
    model.ImageList(_) -> image_list_keybinds()
    model.ImagePreview(_) -> simple_close_keybinds()
    model.DevServerMenu(_) -> simple_close_keybinds()
    model.DiffViewer(_) -> simple_close_keybinds()
    model.MergeChoice(_, _, merge_in_progress) ->
      merge_choice_keybinds(merge_in_progress)
    model.ConfirmDialog(_) -> confirm_keybinds()
    model.PlanningOverlay(state) -> planning_keybinds(state, model)
  }
}

fn action_menu_keybinds() -> List(Node) {
  [
    ui.keybind(key.Esc, model.CloseOverlay),
    ui.keybind(key.Char("s"), model.StartSession),
    ui.keybind(key.Char("S"), model.StartSessionWithWork),
    ui.keybind(key.Char("!"), model.StartSessionYolo),
    ui.keybind(key.Char("a"), model.AttachSession),
    ui.keybind(key.Char("p"), model.PauseSession),
    ui.keybind(key.Char("P"), model.CreatePR),
    ui.keybind(key.Char("r"), model.ResumeSession),
    ui.keybind(key.Ctrl("R"), model.RestartDevServer),
    ui.keybind(key.Char("x"), model.StopSession),
    ui.keybind(key.Char("v"), model.ViewDevServer),
    ui.keybind(key.Char("u"), model.UpdateFromMain),
    ui.keybind(key.Char("m"), model.MergeToMain),
    ui.keybind(key.Char("f"), model.ShowDiff),
    ui.keybind(key.Char("d"), model.DeleteCleanup),
    ui.keybind(key.Char("h"), model.MoveTaskLeft),
    ui.keybind(key.Char("l"), model.MoveTaskRight),
  ]
}

fn sort_menu_keybinds() -> List(Node) {
  [
    ui.keybind(key.Esc, model.CloseOverlay),
    ui.keybind(key.Char("s"), model.SetSort(model.SortBySession)),
    ui.keybind(key.Char("p"), model.SetSort(model.SortByPriority)),
    ui.keybind(key.Char("u"), model.SetSort(model.SortByUpdated)),
  ]
}

fn filter_menu_keybinds() -> List(Node) {
  [
    ui.keybind(key.Esc, model.CloseOverlay),
    ui.keybind(key.Char("s"), model.OpenStatusFilterMenu),
    ui.keybind(key.Char("p"), model.OpenPriorityFilterMenu),
    ui.keybind(key.Char("t"), model.OpenTypeFilterMenu),
    ui.keybind(key.Char("S"), model.OpenSessionFilterMenu),
    ui.keybind(key.Char("e"), model.ToggleHideEpicChildren),
    ui.keybind(key.Char("c"), model.ClearFilters),
  ]
}

fn status_filter_keybinds() -> List(Node) {
  [
    ui.keybind(key.Esc, model.OpenFilterMenu),
    ui.keybind(key.Char("q"), model.CloseOverlay),
    // Note: Can't import task module here due to circular deps,
    // but the messages handle the task.Status internally
  ]
}

fn priority_filter_keybinds() -> List(Node) {
  [
    ui.keybind(key.Esc, model.OpenFilterMenu),
    ui.keybind(key.Char("q"), model.CloseOverlay),
  ]
}

fn type_filter_keybinds() -> List(Node) {
  [
    ui.keybind(key.Esc, model.OpenFilterMenu),
    ui.keybind(key.Char("q"), model.CloseOverlay),
  ]
}

fn session_filter_keybinds() -> List(Node) {
  [
    ui.keybind(key.Esc, model.OpenFilterMenu),
    ui.keybind(key.Char("q"), model.CloseOverlay),
  ]
}

fn settings_keybinds() -> List(Node) {
  [
    ui.keybind(key.Esc, model.CloseOverlay),
    ui.keybind(key.Char("q"), model.CloseOverlay),
    ui.keybind(key.Char("j"), model.SettingsNavigateDown),
    ui.keybind(key.Down, model.SettingsNavigateDown),
    ui.keybind(key.Char("k"), model.SettingsNavigateUp),
    ui.keybind(key.Up, model.SettingsNavigateUp),
    ui.keybind(key.Char(" "), model.SettingsToggleCurrent),
    ui.keybind(key.Enter, model.SettingsToggleCurrent),
  ]
}

fn simple_close_keybinds() -> List(Node) {
  [
    ui.keybind(key.Esc, model.CloseOverlay),
    ui.keybind(key.Char("q"), model.CloseOverlay),
  ]
}

fn project_selector_keybinds() -> List(Node) {
  [
    ui.keybind(key.Esc, model.CloseOverlay),
    ui.keybind(key.Char("q"), model.CloseOverlay),
    ui.keybind(key.Char("1"), model.SelectProject(0)),
    ui.keybind(key.Char("2"), model.SelectProject(1)),
    ui.keybind(key.Char("3"), model.SelectProject(2)),
    ui.keybind(key.Char("4"), model.SelectProject(3)),
    ui.keybind(key.Char("5"), model.SelectProject(4)),
    ui.keybind(key.Char("6"), model.SelectProject(5)),
    ui.keybind(key.Char("7"), model.SelectProject(6)),
    ui.keybind(key.Char("8"), model.SelectProject(7)),
    ui.keybind(key.Char("9"), model.SelectProject(8)),
  ]
}

fn detail_panel_keybinds() -> List(Node) {
  [
    ui.keybind(key.Esc, model.CloseOverlay),
    ui.keybind(key.Char("q"), model.CloseOverlay),
    ui.keybind(key.Char("e"), model.EditBead),
    ui.keybind(key.Char("i"), model.AttachImage),
    ui.keybind(key.Char("I"), model.OpenImageList),
  ]
}

fn image_attach_keybinds() -> List(Node) {
  [
    ui.keybind(key.Esc, model.CloseOverlay),
    ui.keybind(key.Char("p"), model.PasteFromClipboard),
    ui.keybind(key.Char("v"), model.PasteFromClipboard),
    ui.keybind(key.Char("f"), model.SelectFile),
  ]
}

fn image_list_keybinds() -> List(Node) {
  [
    ui.keybind(key.Esc, model.CloseOverlay),
    ui.keybind(key.Char("q"), model.CloseOverlay),
    ui.keybind(key.Char("a"), model.AttachImage),
  ]
}

fn merge_choice_keybinds(merge_in_progress: Bool) -> List(Node) {
  let base = [ui.keybind(key.Esc, model.CloseOverlay)]

  case merge_in_progress {
    True ->
      list.append(base, [
        ui.keybind(key.Char("s"), model.SkipAndAttach),
        ui.keybind(key.Char("a"), model.AbortMerge),
      ])
    False ->
      list.append(base, [
        ui.keybind(key.Char("m"), model.MergeAndAttach),
        ui.keybind(key.Char("s"), model.SkipAndAttach),
      ])
  }
}

fn confirm_keybinds() -> List(Node) {
  [
    ui.keybind(key.Esc, model.CancelAction),
    ui.keybind(key.Char("n"), model.CancelAction),
    ui.keybind(key.Char("y"), model.ConfirmAction),
  ]
}

fn planning_keybinds(
  state: model.PlanningOverlayState,
  _model: Model,
) -> List(Node) {
  case state {
    model.PlanningInput(_) -> [
      ui.keybind(key.Esc, model.PlanningCancel),
      ui.keybind(key.Enter, model.PlanningSubmit),
    ]
    model.PlanningGenerating(_)
    | model.PlanningReviewing(_, _, _)
    | model.PlanningCreatingBeads(_) -> [
      ui.keybind(key.Esc, model.PlanningCancel),
      ui.keybind(key.Char("a"), model.PlanningAttachSession),
    ]
    model.PlanningComplete(_) | model.PlanningError(_) -> [
      ui.keybind(key.Esc, model.CloseOverlay),
      ui.keybind(key.Char("q"), model.CloseOverlay),
      ui.keybind(key.Enter, model.CloseOverlay),
    ]
  }
}

// =============================================================================
// Goto Mode Keybinds
// =============================================================================

fn goto_keybinds() -> List(Node) {
  [
    ui.keybind(key.Esc, model.ExitGoto),
    ui.keybind(key.Char("g"), model.GotoFirst),
    ui.keybind(key.Char("e"), model.GotoLast),
    ui.keybind(key.Char("h"), model.GotoColumn(0)),
    ui.keybind(key.Char("l"), model.GotoColumn(3)),
    ui.keybind(key.Char("p"), model.OpenProjectSelector),
  ]
}

// =============================================================================
// Normal Mode Keybinds
// =============================================================================

fn normal_keybinds(model: Model) -> List(Node) {
  case model.mode {
    Normal -> normal_mode_keybinds(model)
    Select(_) -> select_mode_keybinds()
  }
}

fn normal_mode_keybinds(model: Model) -> List(Node) {
  let navigation = [
    ui.keybind(key.Char("h"), model.MoveLeft),
    ui.keybind(key.Left, model.MoveLeft),
    ui.keybind(key.Char("j"), model.MoveDown),
    ui.keybind(key.Down, model.MoveDown),
    ui.keybind(key.Char("k"), model.MoveUp),
    ui.keybind(key.Up, model.MoveUp),
    ui.keybind(key.Char("l"), model.MoveRight),
    ui.keybind(key.Right, model.MoveRight),
    ui.keybind(key.Ctrl("D"), model.PageDown),
    ui.keybind(key.Ctrl("U"), model.PageUp),
  ]

  let mode_switches = [
    ui.keybind(key.Char(" "), model.OpenActionMenu),
    ui.keybind(key.Char("/"), model.EnterSearch),
    ui.keybind(key.Char("f"), model.OpenFilterMenu),
    ui.keybind(key.Char(","), model.OpenSortMenu),
    ui.keybind(key.Char("v"), model.EnterSelect),
    ui.keybind(key.Char("g"), model.EnterGoto),
  ]

  let quick_actions = [
    ui.keybind(key.Char("?"), model.OpenHelp),
    ui.keybind(key.Char("s"), model.OpenSettings),
    ui.keybind(key.Char("d"), model.OpenDiagnostics),
    ui.keybind(key.Char("p"), model.OpenPlanning),
    ui.keybind(key.Char("L"), model.OpenLogs),
    ui.keybind(key.Char("c"), model.CreateBead),
    ui.keybind(key.Char("C"), model.CreateBeadWithClaude),
    ui.keybind(key.Char("R"), model.ForceRedraw),
    ui.keybind(key.Ctrl("L"), model.ForceRedraw),
  ]

  // Enter key behavior depends on current task type
  let enter_keybind = case model.current_epic {
    Some(_) -> [
      // In epic drill-down, enter opens detail panel
      ui.keybind(key.Enter, model.OpenDetailPanel),
    ]
    None -> [
      // Normal mode - enter drills into epic or opens detail
      // This is handled specially since it depends on task type
      ui.keybind(key.Enter, model.OpenDetailPanel),
    ]
  }

  // Escape exits epic drill-down if active
  let escape_keybind = case model.current_epic {
    Some(_) -> [ui.keybind(key.Esc, model.ExitEpicDrill)]
    None -> []
  }

  // Quit or exit drill-down
  let quit_keybind = case model.current_epic {
    Some(_) -> {
      logger.debug("keybinds: adding q -> ExitEpicDrill (in epic drill-down)")
      [ui.keybind(key.Char("q"), model.ExitEpicDrill)]
    }
    None -> {
      logger.debug("keybinds: adding q -> Quit (normal mode, no epic)")
      [ui.keybind(key.Char("q"), model.Quit)]
    }
  }

  list.flatten([
    navigation,
    mode_switches,
    quick_actions,
    enter_keybind,
    escape_keybind,
    quit_keybind,
  ])
}

fn select_mode_keybinds() -> List(Node) {
  [
    // Navigation
    ui.keybind(key.Char("h"), model.MoveLeft),
    ui.keybind(key.Left, model.MoveLeft),
    ui.keybind(key.Char("j"), model.MoveDown),
    ui.keybind(key.Down, model.MoveDown),
    ui.keybind(key.Char("k"), model.MoveUp),
    ui.keybind(key.Up, model.MoveUp),
    ui.keybind(key.Char("l"), model.MoveRight),
    ui.keybind(key.Right, model.MoveRight),
    // Toggle selection
    ui.keybind(key.Char(" "), model.ToggleSelection),
    // Exit
    ui.keybind(key.Esc, model.ExitSelect),
    ui.keybind(key.Char("v"), model.ExitSelect),
  ]
}
