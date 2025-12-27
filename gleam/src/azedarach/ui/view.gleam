// Main view - renders the entire UI

import gleam/list
import gleam/option.{None, Some}
import gleam/string
import shore/ui
import azedarach/ui/model.{type Model, type Overlay, type Toast, type ToastLevel}
import azedarach/ui/view/board
import azedarach/ui/view/status_bar
import azedarach/ui/view/overlays
import azedarach/ui/view/utils

// Re-export common types and functions from utils for convenience
pub type Node =
  utils.Node

pub const text = utils.text

pub const styled_text = utils.styled_text

pub const bold_text = utils.bold_text

pub const dim_text = utils.dim_text

pub const hbox = utils.hbox

pub const vbox = utils.vbox

pub const bordered_box = utils.bordered_box

pub const empty = utils.empty

pub const pad_right = utils.pad_right

pub const pad_left = utils.pad_left

pub const center = utils.center

pub const truncate = utils.truncate

// Main render function
pub fn render(model: Model) -> Node {
  // Main layout: board + status bar
  let main_content =
    ui.col([
      // Board area (takes remaining height)
      board.render(model),
      // Status bar (1 line at bottom)
      status_bar.render(model),
    ])

  // Overlay on top if present
  let with_overlay = case model.overlay {
    None -> main_content
    Some(overlay) -> render_with_overlay(main_content, overlay, model)
  }

  // Toasts at bottom-right (rendered as part of the content)
  case model.toasts {
    [] -> with_overlay
    toasts -> ui.col([with_overlay, render_toasts(toasts)])
  }
}

fn render_with_overlay(
  background: Node,
  overlay: Overlay,
  model: Model,
) -> Node {
  let overlay_element = overlays.render(overlay, model)

  // Stack overlay on background
  ui.col([background, overlay_element])
}

// =============================================================================
// Toast Rendering
// =============================================================================

fn render_toasts(toasts: List(Toast)) -> Node {
  // Render toasts as a column of styled text boxes
  let toast_nodes = list.map(toasts, render_toast)
  ui.col(toast_nodes)
}

fn render_toast(toast: Toast) -> Node {
  let icon = model.toast_icon(toast.level)
  let prefix = toast_prefix(toast.level)

  // Split message by newlines to support multi-line
  let lines = string.split(toast.message, "\n")

  case lines {
    [] -> ui.text("")
    [first, ..rest] -> {
      // First line with icon and prefix
      let first_line = ui.text(prefix <> " " <> icon <> " " <> first <> " ")

      // Additional lines with indentation
      let rest_lines = list.map(rest, fn(line) {
        ui.text("   " <> line <> " ")
      })

      ui.col([first_line, ..rest_lines])
    }
  }
}

fn toast_prefix(level: ToastLevel) -> String {
  case level {
    model.ErrorLevel -> "[ERROR]"
    model.Warning -> "[WARN]"
    model.Success -> "[OK]"
    model.Info -> "[INFO]"
  }
}
