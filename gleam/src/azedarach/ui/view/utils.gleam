// View utilities - shared helpers for view modules

import gleam/option.{Some}
import gleam/string
import shore
import shore/ui
import shore/style
import azedarach/ui/model.{type Msg}

/// Re-export Node type for use in view modules
pub type Node =
  shore.Node(Msg)

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
