/// Color utilities for LustreTUI - hex parsing and convenience functions
///
/// This module wraps etch's Color type and provides hex color parsing
/// plus named constants for the Catppuccin Macchiato palette.

import etch/style
import gleam/int
import gleam/string

/// Re-export etch's Color type for convenience
pub type Color =
  style.Color

/// Parse hex color string to Etch Rgb color
/// Supports "#rrggbb" and "#rgb" formats
pub fn from_hex(hex: String) -> Result(style.Color, Nil) {
  case parse_hex(hex) {
    Ok(#(r, g, b)) -> Ok(style.Rgb(r, g, b))
    Error(Nil) -> Error(Nil)
  }
}

/// Parse hex to RGB tuple (for cases where you need raw values)
pub fn parse_hex(hex: String) -> Result(#(Int, Int, Int), Nil) {
  // Strip leading '#' if present
  let clean = case string.starts_with(hex, "#") {
    True -> string.drop_start(hex, 1)
    False -> hex
  }

  // Handle 3-char shorthand (expand "abc" to "aabbcc")
  let expanded = case string.length(clean) {
    3 -> expand_shorthand(clean)
    6 -> Ok(clean)
    _ -> Error(Nil)
  }

  case expanded {
    Ok(hex_str) -> parse_6_char_hex(hex_str)
    Error(Nil) -> Error(Nil)
  }
}

/// Create Etch color from RGB values
pub fn rgb(r: Int, g: Int, b: Int) -> style.Color {
  style.Rgb(r, g, b)
}

// Helper: expand 3-char hex to 6-char (e.g., "abc" -> "aabbcc")
fn expand_shorthand(hex: String) -> Result(String, Nil) {
  case string.to_graphemes(hex) {
    [r, g, b] -> Ok(r <> r <> g <> g <> b <> b)
    _ -> Error(Nil)
  }
}

// Helper: parse 6-character hex string to RGB tuple
fn parse_6_char_hex(hex: String) -> Result(#(Int, Int, Int), Nil) {
  let graphemes = string.to_graphemes(hex)
  case graphemes {
    [r1, r2, g1, g2, b1, b2] -> {
      let r_hex = r1 <> r2
      let g_hex = g1 <> g2
      let b_hex = b1 <> b2
      case
        int.base_parse(r_hex, 16),
        int.base_parse(g_hex, 16),
        int.base_parse(b_hex, 16)
      {
        Ok(r), Ok(g), Ok(b) -> Ok(#(r, g, b))
        _, _, _ -> Error(Nil)
      }
    }
    _ -> Error(Nil)
  }
}

// ============================================================================
// Named color constants - Catppuccin Macchiato palette
// ============================================================================

/// Main text color #cad3f5
pub const text = style.Rgb(202, 211, 245)

/// Subtle text color #a5adce
pub const subtext = style.Rgb(165, 173, 206)

/// Blue accent #8aadf4
pub const blue = style.Rgb(138, 173, 244)

/// Green for success/done #a6da95
pub const green = style.Rgb(166, 218, 149)

/// Yellow for warnings/waiting #eed49f
pub const yellow = style.Rgb(238, 212, 159)

/// Red for errors/blocked #ed8796
pub const red = style.Rgb(237, 135, 150)

/// Surface background #363a4f
pub const surface0 = style.Rgb(54, 58, 79)

/// Base background #24273a
pub const base = style.Rgb(36, 39, 58)

/// Mauve accent #c6a0f6
pub const mauve = style.Rgb(198, 160, 246)

/// Peach accent #f5a97f
pub const peach = style.Rgb(245, 169, 127)

/// Lavender accent #b7bdf8
pub const lavender = style.Rgb(183, 189, 248)

/// Teal accent #8bd5ca
pub const teal = style.Rgb(139, 213, 202)

/// Mantle (darker than base) #1e2030
pub const mantle = style.Rgb(30, 32, 48)

/// Surface1 (lighter surface) #494d64
pub const surface1 = style.Rgb(73, 77, 100)
