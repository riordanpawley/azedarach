// Main view - renders the entire UI

import gleam/option.{None, Some}
import shore/layout
import shore/style
import shore/ui
import gleam/int
import gleam/list
import azedarach/ui/model.{type Model}
import azedarach/ui/view/board
import azedarach/ui/view/status_bar
import azedarach/ui/view/overlays
import azedarach/ui/view/utils
import azedarach/ui/keybinds
import azedarach/util/logger
import shore/key

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
  // DEBUG: Minimal test - just keybinds and text
  // This tests if Shore's keybind detection works at all
  logger.debug("view.render: creating MINIMAL test view with q keybind")

  ui.col([
    // Put keybind FIRST - this is how Shore examples do it
    ui.keybind(key.Char("q"), model.Quit),
    ui.text("Press 'q' to quit (minimal test)"),
    ui.text("Press Ctrl+X for Shore's built-in exit"),
  ])
}

// TODO: Add toast rendering back when needed
// Toasts were removed to simplify layout - they were breaking the grid sizing
