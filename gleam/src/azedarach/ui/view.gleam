// Main view - renders the entire UI

import gleam/int
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/string
import shore
import shore/ui
import shore/style
import azedarach/ui/model.{type Model, type Msg, type Overlay}
import azedarach/ui/theme.{type Colors}
import azedarach/ui/view/board
import azedarach/ui/view/status_bar
import azedarach/ui/view/overlays

/// Re-export Node type for use in other view modules
pub type Node =
  shore.Node(Msg)

// Main render function
pub fn render(model: Model) -> Node {
  let colors = model.colors

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

// Helper constructors using Shore's ui module
pub fn text(content: String) -> Node {
  ui.text(content)
}

pub fn styled_text(content: String, fg: String) -> Node {
  ui.text_styled(content, Some(style.hex(fg)), None)
}

pub fn bold_text(content: String, fg: String) -> Node {
  // Shore doesn't have a direct bold - use styled text
  ui.text_styled(content, Some(style.hex(fg)), None)
}

pub fn dim_text(content: String) -> Node {
  // Use a muted color for dim text
  ui.text_styled(content, Some(style.hex("#6e738d")), None)
}

pub fn hbox(children: List(Node)) -> Node {
  ui.row(children)
}

pub fn vbox(children: List(Node)) -> Node {
  ui.col(children)
}

pub fn bordered_box(children: List(Node), fg: String) -> Node {
  ui.box_styled(children, None, Some(style.hex(fg)))
}

pub fn empty() -> Node {
  ui.text("")
}

// Pad string to width
pub fn pad_right(s: String, width: Int) -> String {
  let len = string.length(s)
  case len >= width {
    True -> string.slice(s, 0, width)
    False -> s <> string.repeat(" ", width - len)
  }
}

pub fn pad_left(s: String, width: Int) -> String {
  let len = string.length(s)
  case len >= width {
    True -> string.slice(s, 0, width)
    False -> string.repeat(" ", width - len) <> s
  }
}

pub fn center(s: String, width: Int) -> String {
  let len = string.length(s)
  case len >= width {
    True -> string.slice(s, 0, width)
    False -> {
      let padding = width - len
      let left = padding / 2
      let right = padding - left
      string.repeat(" ", left) <> s <> string.repeat(" ", right)
    }
  }
}

// Truncate with ellipsis
pub fn truncate(s: String, max_len: Int) -> String {
  case string.length(s) > max_len {
    True -> string.slice(s, 0, max_len - 1) <> "…"
    False -> s
  }
}
