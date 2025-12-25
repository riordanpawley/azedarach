// Main view - renders the entire UI

import gleam/option.{type Option, None, Some}
import shore/ui
import azedarach/ui/model.{type Model, type Overlay}
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
  case model.overlay {
    None -> main_content
    Some(overlay) -> render_with_overlay(main_content, overlay, model)
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
