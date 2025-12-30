//// Border drawing for LustreTUI
////
//// Provides border styles using Unicode box-drawing characters
//// and functions to draw borders on a FrameBuffer.

import tui_core/color.{type Color}
import tui_core/framebuffer.{type FrameBuffer}

// =============================================================================
// Border Styles
// =============================================================================

/// Available border styles using Unicode box-drawing characters
pub type BorderStyle {
  None
  Single
  Double
  Rounded
  Bold
  Dashed
}

/// The actual characters for a border style
pub type BorderChars {
  BorderChars(
    top: String,
    bottom: String,
    left: String,
    right: String,
    top_left: String,
    top_right: String,
    bottom_left: String,
    bottom_right: String,
  )
}

// =============================================================================
// Functions
// =============================================================================

/// Get the characters for a border style
pub fn get_chars(style: BorderStyle) -> BorderChars {
  case style {
    None ->
      BorderChars(
        top: " ",
        bottom: " ",
        left: " ",
        right: " ",
        top_left: " ",
        top_right: " ",
        bottom_left: " ",
        bottom_right: " ",
      )
    Single ->
      BorderChars(
        top: "─",
        bottom: "─",
        left: "│",
        right: "│",
        top_left: "┌",
        top_right: "┐",
        bottom_left: "└",
        bottom_right: "┘",
      )
    Double ->
      BorderChars(
        top: "═",
        bottom: "═",
        left: "║",
        right: "║",
        top_left: "╔",
        top_right: "╗",
        bottom_left: "╚",
        bottom_right: "╝",
      )
    Rounded ->
      BorderChars(
        top: "─",
        bottom: "─",
        left: "│",
        right: "│",
        top_left: "╭",
        top_right: "╮",
        bottom_left: "╰",
        bottom_right: "╯",
      )
    Bold ->
      BorderChars(
        top: "━",
        bottom: "━",
        left: "┃",
        right: "┃",
        top_left: "┏",
        top_right: "┓",
        bottom_left: "┗",
        bottom_right: "┛",
      )
    Dashed ->
      BorderChars(
        top: "╌",
        bottom: "╌",
        left: "╎",
        right: "╎",
        top_left: "┌",
        top_right: "┐",
        bottom_left: "└",
        bottom_right: "┘",
      )
  }
}

/// Draw a border on a FrameBuffer
/// x, y: top-left corner position
/// width, height: outer dimensions (including border)
/// Returns the modified FrameBuffer
pub fn draw_border(
  fb: FrameBuffer,
  x: Int,
  y: Int,
  width: Int,
  height: Int,
  style: BorderStyle,
  fg: Color,
  bg: Color,
) -> FrameBuffer {
  // Early return for None style or dimensions too small
  case style {
    None -> fb
    _ -> {
      case width < 2 || height < 2 {
        True -> fb
        False -> draw_border_impl(fb, x, y, width, height, style, fg, bg)
      }
    }
  }
}

fn draw_border_impl(
  fb: FrameBuffer,
  x: Int,
  y: Int,
  width: Int,
  height: Int,
  style: BorderStyle,
  fg: Color,
  bg: Color,
) -> FrameBuffer {
  let chars = get_chars(style)
  let attrs = framebuffer.default_attrs()

  // Draw corners
  let fb =
    framebuffer.set_cell(
      fb,
      x,
      y,
      framebuffer.Cell(char: chars.top_left, fg: fg, bg: bg, attrs: attrs),
    )
  let fb =
    framebuffer.set_cell(
      fb,
      x + width - 1,
      y,
      framebuffer.Cell(char: chars.top_right, fg: fg, bg: bg, attrs: attrs),
    )
  let fb =
    framebuffer.set_cell(
      fb,
      x,
      y + height - 1,
      framebuffer.Cell(char: chars.bottom_left, fg: fg, bg: bg, attrs: attrs),
    )
  let fb =
    framebuffer.set_cell(
      fb,
      x + width - 1,
      y + height - 1,
      framebuffer.Cell(char: chars.bottom_right, fg: fg, bg: bg, attrs: attrs),
    )

  // Draw top and bottom edges
  let fb = draw_horizontal_edge(fb, x + 1, y, width - 2, chars.top, fg, bg, attrs)
  let fb =
    draw_horizontal_edge(
      fb,
      x + 1,
      y + height - 1,
      width - 2,
      chars.bottom,
      fg,
      bg,
      attrs,
    )

  // Draw left and right edges
  let fb = draw_vertical_edge(fb, x, y + 1, height - 2, chars.left, fg, bg, attrs)
  let fb =
    draw_vertical_edge(
      fb,
      x + width - 1,
      y + 1,
      height - 2,
      chars.right,
      fg,
      bg,
      attrs,
    )

  fb
}

fn draw_horizontal_edge(
  fb: FrameBuffer,
  x: Int,
  y: Int,
  count: Int,
  char: String,
  fg: Color,
  bg: Color,
  attrs: framebuffer.Attrs,
) -> FrameBuffer {
  case count <= 0 {
    True -> fb
    False -> {
      let fb =
        framebuffer.set_cell(
          fb,
          x,
          y,
          framebuffer.Cell(char: char, fg: fg, bg: bg, attrs: attrs),
        )
      draw_horizontal_edge(fb, x + 1, y, count - 1, char, fg, bg, attrs)
    }
  }
}

fn draw_vertical_edge(
  fb: FrameBuffer,
  x: Int,
  y: Int,
  count: Int,
  char: String,
  fg: Color,
  bg: Color,
  attrs: framebuffer.Attrs,
) -> FrameBuffer {
  case count <= 0 {
    True -> fb
    False -> {
      let fb =
        framebuffer.set_cell(
          fb,
          x,
          y,
          framebuffer.Cell(char: char, fg: fg, bg: bg, attrs: attrs),
        )
      draw_vertical_edge(fb, x, y + 1, count - 1, char, fg, bg, attrs)
    }
  }
}

/// Get the inset caused by a border (how much inner content is reduced)
/// Returns #(horizontal_inset, vertical_inset) - typically (2, 2) for bordered, (0, 0) for None
pub fn border_inset(style: BorderStyle) -> #(Int, Int) {
  case style {
    None -> #(0, 0)
    Single -> #(2, 2)
    Double -> #(2, 2)
    Rounded -> #(2, 2)
    Bold -> #(2, 2)
    Dashed -> #(2, 2)
  }
}

/// Check if a border style actually draws anything
pub fn has_border(style: BorderStyle) -> Bool {
  case style {
    None -> False
    _ -> True
  }
}
