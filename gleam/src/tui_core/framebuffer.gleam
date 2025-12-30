//// FrameBuffer - 2D grid of cells for terminal rendering.
////
//// Provides a buffer of character cells with color and attribute information,
//// plus dirty tracking for efficient incremental rendering.

import gleam/list
import gleam/result
import gleam/set.{type Set}
import gleam/string
import tui_core/color.{type Color}
import tui_core/text

/// Text attributes (bold, dim, italic, underline)
pub type Attrs {
  Attrs(bold: Bool, dim: Bool, italic: Bool, underline: Bool)
}

/// A single cell in the framebuffer
pub type Cell {
  Cell(char: String, fg: Color, bg: Color, attrs: Attrs)
}

/// 2D cell grid with dirty tracking
pub opaque type FrameBuffer {
  FrameBuffer(
    width: Int,
    height: Int,
    cells: List(Cell),
    dirty: Set(Int),
  )
}

/// Default text attributes (all off)
pub fn default_attrs() -> Attrs {
  Attrs(bold: False, dim: False, italic: False, underline: False)
}

/// Default cell (space with default colors)
pub fn default_cell() -> Cell {
  Cell(char: " ", fg: color.text, bg: color.base, attrs: default_attrs())
}

/// Create a new framebuffer with given dimensions, filled with default cells
pub fn new(width: Int, height: Int) -> FrameBuffer {
  let safe_width = case width < 0 {
    True -> 0
    False -> width
  }
  let safe_height = case height < 0 {
    True -> 0
    False -> height
  }
  let size = safe_width * safe_height
  let cells = list.repeat(default_cell(), size)
  FrameBuffer(width: safe_width, height: safe_height, cells: cells, dirty: set.new())
}

/// Get the width of the framebuffer
pub fn width(fb: FrameBuffer) -> Int {
  fb.width
}

/// Get the height of the framebuffer
pub fn height(fb: FrameBuffer) -> Int {
  fb.height
}

/// Convert (x, y) position to cell index
/// Returns Error(Nil) if position is out of bounds
pub fn pos_to_index(fb: FrameBuffer, x: Int, y: Int) -> Result(Int, Nil) {
  case x >= 0 && x < fb.width && y >= 0 && y < fb.height {
    True -> Ok(y * fb.width + x)
    False -> Error(Nil)
  }
}

/// Convert cell index to (x, y) position
pub fn index_to_pos(fb: FrameBuffer, index: Int) -> #(Int, Int) {
  let x = index % fb.width
  let y = index / fb.width
  #(x, y)
}

/// Get the cell at position (x, y)
/// Returns Error(Nil) if position is out of bounds
pub fn get_cell(fb: FrameBuffer, x: Int, y: Int) -> Result(Cell, Nil) {
  case pos_to_index(fb, x, y) {
    Ok(index) -> list_at(fb.cells, index)
    Error(Nil) -> Error(Nil)
  }
}

/// Set the cell at position (x, y)
/// Marks the cell as dirty. Returns unchanged framebuffer if out of bounds.
pub fn set_cell(fb: FrameBuffer, x: Int, y: Int, cell: Cell) -> FrameBuffer {
  case pos_to_index(fb, x, y) {
    Ok(index) -> {
      let new_cells = list_set(fb.cells, index, cell)
      let new_dirty = set.insert(fb.dirty, index)
      FrameBuffer(..fb, cells: new_cells, dirty: new_dirty)
    }
    Error(Nil) -> fb
  }
}

/// Clear the entire framebuffer (fill with default cells and mark all dirty)
pub fn clear(fb: FrameBuffer) -> FrameBuffer {
  let size = fb.width * fb.height
  let cells = list.repeat(default_cell(), size)
  // Mark all cells as dirty
  let dirty = list.range(0, size - 1) |> set.from_list
  FrameBuffer(..fb, cells: cells, dirty: dirty)
}

/// Resize the framebuffer to new dimensions
/// Preserves existing cells where possible, fills new areas with default cells
pub fn resize(fb: FrameBuffer, new_width: Int, new_height: Int) -> FrameBuffer {
  let safe_width = case new_width < 0 {
    True -> 0
    False -> new_width
  }
  let safe_height = case new_height < 0 {
    True -> 0
    False -> new_height
  }

  // Build new cell list
  let new_size = safe_width * safe_height
  let new_cells = build_resized_cells(fb, safe_width, safe_height, 0, new_size, [])

  // Mark all as dirty since layout changed
  let dirty = list.range(0, new_size - 1) |> set.from_list

  FrameBuffer(width: safe_width, height: safe_height, cells: new_cells, dirty: dirty)
}

fn build_resized_cells(
  old_fb: FrameBuffer,
  new_width: Int,
  new_height: Int,
  index: Int,
  size: Int,
  acc: List(Cell),
) -> List(Cell) {
  case index >= size {
    True -> list.reverse(acc)
    False -> {
      let x = index % new_width
      let y = index / new_width
      let cell = case y < new_height && x < new_width {
        True ->
          get_cell(old_fb, x, y)
          |> result.unwrap(default_cell())
        False -> default_cell()
      }
      build_resized_cells(old_fb, new_width, new_height, index + 1, size, [cell, ..acc])
    }
  }
}

/// Fill a rectangular region with the given cell
pub fn fill_rect(
  fb: FrameBuffer,
  x: Int,
  y: Int,
  w: Int,
  h: Int,
  cell: Cell,
) -> FrameBuffer {
  // Clamp to valid range
  let x1 = int_max(0, x)
  let y1 = int_max(0, y)
  let x2 = int_min(fb.width, x + w)
  let y2 = int_min(fb.height, y + h)

  fill_rect_loop(fb, x1, y1, x2, y2, x1, y1, cell)
}

fn fill_rect_loop(
  fb: FrameBuffer,
  x1: Int,
  y1: Int,
  x2: Int,
  y2: Int,
  curr_x: Int,
  curr_y: Int,
  cell: Cell,
) -> FrameBuffer {
  // Note: y1 is passed through for recursion but not used in computation
  let _ = y1
  case curr_y >= y2 {
    True -> fb
    False -> {
      case curr_x >= x2 {
        True -> fill_rect_loop(fb, x1, y1, x2, y2, x1, curr_y + 1, cell)
        False -> {
          let new_fb = set_cell(fb, curr_x, curr_y, cell)
          fill_rect_loop(new_fb, x1, y1, x2, y2, curr_x + 1, curr_y, cell)
        }
      }
    }
  }
}

/// Draw text at position (x, y) with given colors
/// Advances x by the display width of each grapheme for correct Unicode handling
pub fn draw_text(
  fb: FrameBuffer,
  x: Int,
  y: Int,
  text_str: String,
  fg: Color,
  bg: Color,
) -> FrameBuffer {
  let graphemes = string.to_graphemes(text_str)
  draw_graphemes(fb, x, y, graphemes, fg, bg)
}

fn draw_graphemes(
  fb: FrameBuffer,
  x: Int,
  y: Int,
  graphemes: List(String),
  fg: Color,
  bg: Color,
) -> FrameBuffer {
  case graphemes {
    [] -> fb
    [g, ..rest] -> {
      let char_width = text.display_width(g)
      // Only draw if x is within bounds
      case x >= 0 && x < fb.width && y >= 0 && y < fb.height {
        True -> {
          let cell = Cell(char: g, fg: fg, bg: bg, attrs: default_attrs())
          let new_fb = set_cell(fb, x, y, cell)
          // For wide characters (like CJK), fill remaining cells with space
          let new_fb2 = fill_wide_char_padding(new_fb, x + 1, y, char_width - 1, fg, bg)
          draw_graphemes(new_fb2, x + char_width, y, rest, fg, bg)
        }
        False -> {
          // Still advance x even if out of bounds (for text that starts off-screen)
          draw_graphemes(fb, x + char_width, y, rest, fg, bg)
        }
      }
    }
  }
}

/// Fill cells after a wide character with continuation markers (empty cells)
fn fill_wide_char_padding(
  fb: FrameBuffer,
  x: Int,
  y: Int,
  count: Int,
  fg: Color,
  bg: Color,
) -> FrameBuffer {
  case count <= 0 {
    True -> fb
    False -> {
      case x >= 0 && x < fb.width && y >= 0 && y < fb.height {
        True -> {
          // Use empty string as continuation marker for wide chars
          let cell = Cell(char: "", fg: fg, bg: bg, attrs: default_attrs())
          let new_fb = set_cell(fb, x, y, cell)
          fill_wide_char_padding(new_fb, x + 1, y, count - 1, fg, bg)
        }
        False -> fb
      }
    }
  }
}

/// Get all dirty cells as a list of (x, y, cell) tuples
pub fn get_dirty_cells(fb: FrameBuffer) -> List(#(Int, Int, Cell)) {
  fb.dirty
  |> set.to_list
  |> list.filter_map(fn(index) {
    case list_at(fb.cells, index) {
      Ok(cell) -> {
        let #(x, y) = index_to_pos(fb, index)
        Ok(#(x, y, cell))
      }
      Error(Nil) -> Error(Nil)
    }
  })
}

/// Clear the dirty set (call after rendering)
pub fn clear_dirty(fb: FrameBuffer) -> FrameBuffer {
  FrameBuffer(..fb, dirty: set.new())
}

/// Check if any cells are dirty
pub fn is_dirty(fb: FrameBuffer) -> Bool {
  !set.is_empty(fb.dirty)
}

// =============================================================================
// Helper functions
// =============================================================================

/// Get element at index from list
fn list_at(lst: List(a), index: Int) -> Result(a, Nil) {
  case index < 0 {
    True -> Error(Nil)
    False -> list_at_loop(lst, index)
  }
}

fn list_at_loop(lst: List(a), index: Int) -> Result(a, Nil) {
  case lst {
    [] -> Error(Nil)
    [head, ..tail] ->
      case index == 0 {
        True -> Ok(head)
        False -> list_at_loop(tail, index - 1)
      }
  }
}

/// Set element at index in list
fn list_set(lst: List(a), index: Int, value: a) -> List(a) {
  list_set_loop(lst, index, value, [])
}

fn list_set_loop(lst: List(a), index: Int, value: a, acc: List(a)) -> List(a) {
  case lst {
    [] -> list.reverse(acc)
    [head, ..tail] ->
      case index == 0 {
        True -> list.append(list.reverse([value, ..acc]), tail)
        False -> list_set_loop(tail, index - 1, value, [head, ..acc])
      }
  }
}

fn int_max(a: Int, b: Int) -> Int {
  case a > b {
    True -> a
    False -> b
  }
}

fn int_min(a: Int, b: Int) -> Int {
  case a < b {
    True -> a
    False -> b
  }
}
