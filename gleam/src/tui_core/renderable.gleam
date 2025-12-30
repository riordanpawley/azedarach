//// Renderable - Core type definitions for LustreTUI UI elements.
////
//// This module defines the fundamental types for building terminal UIs:
//// - Box: Container with flexbox-style layout
//// - Text: Text content with styling
//// - Span: Inline styled text (for use within Text)
////
//// Layout algorithm is implemented separately - this is pure type definitions.

import gleam/option.{type Option, None}
import tui_core/color.{type Color}

// =============================================================================
// Size Types
// =============================================================================

/// How an element sizes itself
pub type Size {
  /// Fixed size in cells
  Px(Int)
  /// Percentage of parent (0-100)
  Pct(Int)
  /// Take remaining space
  Fill
  /// Size to content
  Auto
}

// =============================================================================
// Flexbox-style Layout
// =============================================================================

/// Direction children are laid out
pub type FlexDirection {
  /// Children laid out horizontally (left to right)
  Row
  /// Children laid out vertically (top to bottom)
  Column
}

/// How to distribute space along the main axis
pub type Justify {
  /// Pack children at start
  JustifyStart
  /// Pack children at end
  JustifyEnd
  /// Center children
  JustifyCenter
  /// Distribute space between children
  SpaceBetween
  /// Distribute space around children
  SpaceAround
}

/// How to align children on the cross axis
pub type Align {
  /// Align to start of cross axis
  AlignStart
  /// Align to end of cross axis
  AlignEnd
  /// Center on cross axis
  AlignCenter
  /// Stretch to fill cross axis
  Stretch
}

// =============================================================================
// Edges (padding, margin)
// =============================================================================

/// Edge values for padding and margin
pub type Edges {
  Edges(top: Int, right: Int, bottom: Int, left: Int)
}

/// All edges same value
pub fn edges_all(n: Int) -> Edges {
  Edges(top: n, right: n, bottom: n, left: n)
}

/// Horizontal (left/right) and vertical (top/bottom)
pub fn edges_xy(x: Int, y: Int) -> Edges {
  Edges(top: y, right: x, bottom: y, left: x)
}

/// All edges zero
pub fn edges_none() -> Edges {
  Edges(top: 0, right: 0, bottom: 0, left: 0)
}

// =============================================================================
// Styles
// =============================================================================

/// Style for Box elements
pub type BoxStyle {
  BoxStyle(
    width: Size,
    height: Size,
    padding: Edges,
    margin: Edges,
    flex_direction: FlexDirection,
    justify_content: Justify,
    align_items: Align,
    gap: Int,
    fg: Option(Color),
    bg: Option(Color),
  )
}

/// Style for Text/Span elements
pub type TextStyle {
  TextStyle(
    fg: Option(Color),
    bg: Option(Color),
    bold: Bool,
    dim: Bool,
    italic: Bool,
    underline: Bool,
  )
}

/// Default box style
/// Auto width/height, no padding/margin, Column direction, Start justify/align, gap 0, no colors
pub fn default_box_style() -> BoxStyle {
  BoxStyle(
    width: Auto,
    height: Auto,
    padding: edges_none(),
    margin: edges_none(),
    flex_direction: Column,
    justify_content: JustifyStart,
    align_items: AlignStart,
    gap: 0,
    fg: None,
    bg: None,
  )
}

/// Default text style
/// No colors (inherit from parent), all attributes False
pub fn default_text_style() -> TextStyle {
  TextStyle(
    fg: None,
    bg: None,
    bold: False,
    dim: False,
    italic: False,
    underline: False,
  )
}

// =============================================================================
// Renderable Types
// =============================================================================

/// The core renderable type - can be Box, Text, or Span
pub type Renderable {
  /// Container element with flexbox-style layout
  Box(children: List(Renderable), style: BoxStyle)
  /// Text content with styling
  Text(content: String, style: TextStyle)
  /// Inline styled text (for use within Text)
  Span(content: String, style: TextStyle)
}

// =============================================================================
// Builder helpers
// =============================================================================

/// Create a box with default style
pub fn box(children: List(Renderable)) -> Renderable {
  Box(children: children, style: default_box_style())
}

/// Create a box with custom style
pub fn box_styled(children: List(Renderable), style: BoxStyle) -> Renderable {
  Box(children: children, style: style)
}

/// Create a row (box with Row flex direction)
pub fn row(children: List(Renderable)) -> Renderable {
  let style = BoxStyle(..default_box_style(), flex_direction: Row)
  Box(children: children, style: style)
}

/// Create a column (box with Column flex direction)
pub fn column(children: List(Renderable)) -> Renderable {
  Box(children: children, style: default_box_style())
}

/// Create text with default style
pub fn text(content: String) -> Renderable {
  Text(content: content, style: default_text_style())
}

/// Create text with custom style
pub fn text_styled(content: String, style: TextStyle) -> Renderable {
  Text(content: content, style: style)
}

/// Create span with default style
pub fn span(content: String) -> Renderable {
  Span(content: content, style: default_text_style())
}

/// Create span with custom style
pub fn span_styled(content: String, style: TextStyle) -> Renderable {
  Span(content: content, style: style)
}
