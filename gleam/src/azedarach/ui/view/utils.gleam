// View utilities - shared helpers for view modules

import gleam/option.{None, Some}
import gleam/string
import shore
import shore/style
import shore/ui
import azedarach/ui/model.{type Msg}

/// Re-export Node type for use in view modules
pub type Node =
  shore.Node(Msg)

// Helper constructors using Shore's ui module
pub fn text(content: String) -> Node {
  ui.text(content)
}

/// Map hex color strings to ANSI colors
/// Shore only supports: Black, Red, Green, Yellow, Blue, Magenta, Cyan, White
fn hex_to_ansi(hex: String) -> style.Color {
  // Map Catppuccin colors to closest ANSI equivalents
  case hex {
    // Reds/Pinks → Red or Magenta
    "#ed8796" | "#ee99a0" | "#f38ba8" | "#eba0ac" | "#e78284" | "#ea999c" | "#d20f39" | "#e64553" -> style.Red
    "#f5bde6" | "#f5c2e7" | "#f4b8e4" | "#ea76cb" -> style.Magenta

    // Oranges/Peaches → Yellow (closest we have)
    "#f5a97f" | "#fab387" | "#ef9f76" | "#fe640b" -> style.Yellow

    // Yellows → Yellow
    "#eed49f" | "#f9e2af" | "#e5c890" | "#df8e1d" -> style.Yellow

    // Greens → Green
    "#a6da95" | "#a6e3a1" | "#a6d189" | "#40a02b" -> style.Green

    // Teals/Cyans → Cyan
    "#8bd5ca" | "#94e2d5" | "#81c8be" | "#179299" -> style.Cyan
    "#91d7e3" | "#89dceb" | "#99d1db" | "#04a5e5" -> style.Cyan
    "#7dc4e4" | "#74c7ec" | "#85c1dc" | "#209fb5" -> style.Cyan

    // Blues → Blue
    "#8aadf4" | "#89b4fa" | "#8caaee" | "#1e66f5" -> style.Blue

    // Purples/Mauves → Magenta
    "#c6a0f6" | "#cba6f7" | "#ca9ee6" | "#8839ef" -> style.Magenta
    "#b7bdf8" | "#b4befe" | "#babbf1" | "#7287fd" -> style.Blue  // Lavender closer to blue

    // Rosewaters/Flamingos → Red (light pink-ish)
    "#f4dbd6" | "#f5e0dc" | "#f2d5cf" | "#dc8a78" -> style.Red
    "#f0c6c6" | "#f2cdcd" | "#eebebe" | "#dd7878" -> style.Red

    // Grays/Surfaces → White (closest for light grays)
    "#cad3f5" | "#cdd6f4" | "#c6d0f5" | "#4c4f69" -> style.White  // text
    "#a5adcb" | "#a6adc8" | "#a5adce" | "#6c6f85" -> style.White  // subtext0
    "#b8c0e0" | "#bac2de" | "#b5bfe2" | "#5c5f77" -> style.White  // subtext1

    // Overlay grays → Cyan (visible but muted)
    "#6e738d" | "#6c7086" | "#737994" | "#9ca0b0" -> style.Cyan
    "#8087a2" | "#7f849c" | "#838ba7" | "#8c8fa1" -> style.Cyan
    "#939ab7" | "#9399b2" | "#949cbb" | "#7c7f93" -> style.White

    // Surface/darker grays → Blue (for contrast)
    "#363a4f" | "#313244" | "#414559" | "#ccd0da" -> style.Blue
    "#494d64" | "#45475a" | "#51576d" | "#bcc0cc" -> style.Blue
    "#5b6078" | "#585b70" | "#626880" | "#acb0be" -> style.Cyan

    // Base/backgrounds → Black
    "#24273a" | "#1e1e2e" | "#303446" | "#eff1f5" -> style.Black
    "#1e2030" | "#181825" | "#292c3c" | "#e6e9ef" -> style.Black
    "#181926" | "#11111b" | "#232634" | "#dce0e8" -> style.Black

    // Default fallback
    _ -> style.White
  }
}

pub fn styled_text(content: String, fg: String) -> Node {
  // Map hex color to ANSI and use Shore's text_styled
  let ansi_color = hex_to_ansi(fg)
  ui.text_styled(content, Some(ansi_color), None)
}

pub fn bold_text(content: String, fg: String) -> Node {
  // Shore doesn't have bold attribute, but we can at least color it
  styled_text(content, fg)
}

pub fn dim_text(content: String) -> Node {
  // Use cyan for dim text (muted but visible)
  ui.text_styled(content, Some(style.Cyan), None)
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
