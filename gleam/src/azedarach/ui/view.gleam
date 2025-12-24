// Main view - renders the entire UI

import gleam/int
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/string
import azedarach/ui/model.{type Model, type Overlay}
import azedarach/ui/theme.{type Colors}
import azedarach/ui/view/board
import azedarach/ui/view/status_bar
import azedarach/ui/view/overlays

// Shore element types (simplified - actual types from Shore)
pub type Element {
  Box(children: List(Element), props: BoxProps)
  Text(content: String, props: TextProps)
  Empty
}

pub type BoxProps {
  BoxProps(
    direction: Direction,
    width: Option(Int),
    height: Option(Int),
    padding: Int,
    border: Bool,
    bg: Option(String),
    fg: Option(String),
  )
}

pub type TextProps {
  TextProps(
    fg: Option(String),
    bg: Option(String),
    bold: Bool,
    dim: Bool,
  )
}

pub type Direction {
  Row
  Column
}

// Main render function
pub fn render(model: Model) -> Element {
  let #(width, height) = model.terminal_size
  let colors = model.colors

  // Main layout: board + status bar
  let main_content =
    Box(
      [
        // Board area (takes remaining height)
        board.render(model),
        // Status bar (1 line at bottom)
        status_bar.render(model),
      ],
      BoxProps(
        direction: Column,
        width: Some(width),
        height: Some(height),
        padding: 0,
        border: False,
        bg: Some(colors.base),
        fg: Some(colors.text),
      ),
    )

  // Overlay on top if present
  case model.overlay {
    None -> main_content
    Some(overlay) -> render_with_overlay(main_content, overlay, model)
  }
}

fn render_with_overlay(
  background: Element,
  overlay: Overlay,
  model: Model,
) -> Element {
  let overlay_element = overlays.render(overlay, model)

  // Stack overlay on background
  Box(
    [background, overlay_element],
    BoxProps(
      direction: Column,
      width: None,
      height: None,
      padding: 0,
      border: False,
      bg: None,
      fg: None,
    ),
  )
}

// Helper constructors
pub fn text(content: String) -> Element {
  Text(content, TextProps(fg: None, bg: None, bold: False, dim: False))
}

pub fn styled_text(content: String, fg: String) -> Element {
  Text(content, TextProps(fg: Some(fg), bg: None, bold: False, dim: False))
}

pub fn bold_text(content: String, fg: String) -> Element {
  Text(content, TextProps(fg: Some(fg), bg: None, bold: True, dim: False))
}

pub fn dim_text(content: String) -> Element {
  Text(content, TextProps(fg: None, bg: None, bold: False, dim: True))
}

pub fn hbox(children: List(Element)) -> Element {
  Box(
    children,
    BoxProps(
      direction: Row,
      width: None,
      height: None,
      padding: 0,
      border: False,
      bg: None,
      fg: None,
    ),
  )
}

pub fn vbox(children: List(Element)) -> Element {
  Box(
    children,
    BoxProps(
      direction: Column,
      width: None,
      height: None,
      padding: 0,
      border: False,
      bg: None,
      fg: None,
    ),
  )
}

pub fn bordered_box(children: List(Element), fg: String) -> Element {
  Box(
    children,
    BoxProps(
      direction: Column,
      width: None,
      height: None,
      padding: 1,
      border: True,
      bg: None,
      fg: Some(fg),
    ),
  )
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
