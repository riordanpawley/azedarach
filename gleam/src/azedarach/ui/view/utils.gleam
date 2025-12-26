// View utilities - shared helpers for view modules

import gleam/option.{None}
import gleam/string
import shore
import shore/ui
import azedarach/ui/model.{type Msg}

/// Re-export Node type for use in view modules
pub type Node =
  shore.Node(Msg)

// Helper constructors using Shore's ui module
pub fn text(content: String) -> Node {
  ui.text(content)
}

pub fn styled_text(content: String, _fg: String) -> Node {
  // Shore only supports basic ANSI colors, use text for now
  ui.text(content)
}

pub fn bold_text(content: String, _fg: String) -> Node {
  // Shore doesn't have a direct bold or hex colors - use plain text
  ui.text(content)
}

pub fn dim_text(content: String) -> Node {
  // Use plain text since Shore doesn't support hex colors
  ui.text(content)
}

pub fn hbox(children: List(Node)) -> Node {
  ui.row(children)
}

pub fn vbox(children: List(Node)) -> Node {
  ui.col(children)
}

pub fn bordered_box(children: List(Node), _fg: String) -> Node {
  // Shore doesn't support hex colors for borders, use plain box
  ui.box_styled(children, None, None)
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
