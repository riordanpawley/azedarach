//// Text utilities for TUI rendering with Unicode-aware width calculation.
////
//// This module provides functions for measuring and manipulating text
//// in terminal environments, correctly handling Unicode, CJK characters,
//// and emoji.

import gleam/int
import gleam/list
import gleam/order
import gleam/string
import string_width

// Re-export types that users might need
pub type Alignment {
  Left
  Right
  Center
}

/// Get display width of text in terminal cells.
/// Handles Unicode, CJK, emoji correctly.
///
/// ## Examples
///
/// ```gleam
/// display_width("hello")  // -> 5
/// display_width("こんにちは")  // -> 10 (CJK characters are double-width)
/// display_width("")  // -> 0
/// ```
pub fn display_width(text: String) -> Int {
  string_width.line(text)
}

/// Truncate text to fit in max_width cells, adding ellipsis if needed.
///
/// If the text is already within max_width, returns it unchanged.
/// Otherwise, truncates grapheme by grapheme until text + ellipsis fits.
///
/// ## Examples
///
/// ```gleam
/// truncate("hello world", 8, "...")  // -> "hello..."
/// truncate("hi", 8, "...")  // -> "hi"
/// truncate("こんにちは", 6, "...")  // -> "こん..."
/// ```
pub fn truncate(text: String, max_width: Int, ellipsis: String) -> String {
  let text_width = display_width(text)
  let ellipsis_width = display_width(ellipsis)

  // If text fits, return as-is
  case text_width <= max_width {
    True -> text
    False -> {
      // Need to truncate - find how many graphemes we can keep
      let target_width = max_width - ellipsis_width
      case target_width <= 0 {
        True -> ellipsis |> string.slice(0, max_width)
        False -> truncate_to_width(text, target_width) <> ellipsis
      }
    }
  }
}

/// Helper: truncate text to fit within target_width cells
fn truncate_to_width(text: String, target_width: Int) -> String {
  let graphemes = string.to_graphemes(text)
  truncate_graphemes(graphemes, target_width, [])
}

fn truncate_graphemes(
  graphemes: List(String),
  remaining_width: Int,
  acc: List(String),
) -> String {
  case graphemes {
    [] -> acc |> list.reverse |> string.concat
    [g, ..rest] -> {
      let g_width = display_width(g)
      case g_width <= remaining_width {
        True ->
          truncate_graphemes(rest, remaining_width - g_width, [g, ..acc])
        False -> acc |> list.reverse |> string.concat
      }
    }
  }
}

/// Wrap text to fit within max_width, respecting word boundaries.
///
/// Returns a list of lines, each fitting within max_width cells.
/// Long words that exceed max_width are broken at character boundaries.
///
/// ## Examples
///
/// ```gleam
/// wrap("hello world foo", 6)  // -> ["hello", "world", "foo"]
/// wrap("hi", 10)  // -> ["hi"]
/// ```
pub fn wrap(text: String, max_width: Int) -> List(String) {
  case max_width <= 0 {
    True -> []
    False -> {
      let words = string.split(text, " ")
      wrap_words(words, max_width, [], [])
        |> list.reverse
    }
  }
}

fn wrap_words(
  words: List(String),
  max_width: Int,
  current_line: List(String),
  lines: List(String),
) -> List(String) {
  case words {
    [] -> {
      // Emit any remaining current line
      case current_line {
        [] -> lines
        _ -> [
          current_line |> list.reverse |> string.join(" "),
          ..lines
        ]
      }
    }
    [word, ..rest] -> {
      let word_width = display_width(word)

      case current_line {
        [] -> {
          // Starting a new line
          case word_width <= max_width {
            True ->
              // Word fits on a line
              wrap_words(rest, max_width, [word], lines)
            False -> {
              // Word is too long, break it
              let #(broken_lines, remainder) = break_long_word(word, max_width)
              let new_lines = list.append(list.reverse(broken_lines), lines)
              case remainder {
                "" -> wrap_words(rest, max_width, [], new_lines)
                _ -> wrap_words(rest, max_width, [remainder], new_lines)
              }
            }
          }
        }
        _ -> {
          // Have existing content on current line
          let current_text =
            current_line |> list.reverse |> string.join(" ")
          let current_width = display_width(current_text)
          // +1 for space between words
          let new_width = current_width + 1 + word_width

          case new_width <= max_width {
            True ->
              // Word fits on current line
              wrap_words(rest, max_width, [word, ..current_line], lines)
            False -> {
              // Word doesn't fit, start new line
              let completed_line = current_text
              case word_width <= max_width {
                True ->
                  wrap_words(rest, max_width, [word], [completed_line, ..lines])
                False -> {
                  // Word is too long even for a new line
                  let #(broken_lines, remainder) =
                    break_long_word(word, max_width)
                  let new_lines =
                    list.append(list.reverse(broken_lines), [
                      completed_line,
                      ..lines
                    ])
                  case remainder {
                    "" -> wrap_words(rest, max_width, [], new_lines)
                    _ -> wrap_words(rest, max_width, [remainder], new_lines)
                  }
                }
              }
            }
          }
        }
      }
    }
  }
}

/// Break a long word into lines that fit max_width, returning remaining fragment
fn break_long_word(
  word: String,
  max_width: Int,
) -> #(List(String), String) {
  break_word_loop(string.to_graphemes(word), max_width, [], [])
}

fn break_word_loop(
  graphemes: List(String),
  max_width: Int,
  current_line: List(String),
  completed_lines: List(String),
) -> #(List(String), String) {
  case graphemes {
    [] -> {
      // Return completed lines and any remainder
      let remainder = current_line |> list.reverse |> string.concat
      #(completed_lines, remainder)
    }
    [g, ..rest] -> {
      let current_text = current_line |> list.reverse |> string.concat
      let current_width = display_width(current_text)
      let g_width = display_width(g)

      case current_width + g_width <= max_width {
        True ->
          // Grapheme fits on current line
          break_word_loop(rest, max_width, [g, ..current_line], completed_lines)
        False -> {
          // Need to start a new line
          let completed_line = current_text
          break_word_loop(rest, max_width, [g], [
            completed_line,
            ..completed_lines
          ])
        }
      }
    }
  }
}

/// Pad text to exactly N cells on the right.
///
/// If text is already wider than width, returns text unchanged.
///
/// ## Examples
///
/// ```gleam
/// pad_right("hi", 5, " ")  // -> "hi   "
/// pad_right("hello", 3, " ")  // -> "hello" (no truncation)
/// ```
pub fn pad_right(text: String, width: Int, char: String) -> String {
  let text_width = display_width(text)
  let padding_needed = width - text_width

  case padding_needed <= 0 {
    True -> text
    False -> {
      let padding = repeat_char_to_width(char, padding_needed)
      text <> padding
    }
  }
}

/// Pad text to exactly N cells on the left.
///
/// If text is already wider than width, returns text unchanged.
///
/// ## Examples
///
/// ```gleam
/// pad_left("hi", 5, " ")  // -> "   hi"
/// pad_left("hello", 3, " ")  // -> "hello" (no truncation)
/// ```
pub fn pad_left(text: String, width: Int, char: String) -> String {
  let text_width = display_width(text)
  let padding_needed = width - text_width

  case padding_needed <= 0 {
    True -> text
    False -> {
      let padding = repeat_char_to_width(char, padding_needed)
      padding <> text
    }
  }
}

/// Pad text centered within N cells.
///
/// If text is already wider than width, returns text unchanged.
/// When padding is odd, the extra cell goes on the right.
///
/// ## Examples
///
/// ```gleam
/// pad_center("hi", 6, " ")  // -> "  hi  "
/// pad_center("hi", 5, " ")  // -> " hi  " (extra space on right)
/// ```
pub fn pad_center(text: String, width: Int, char: String) -> String {
  let text_width = display_width(text)
  let padding_needed = width - text_width

  case padding_needed <= 0 {
    True -> text
    False -> {
      let left_padding = padding_needed / 2
      let right_padding = padding_needed - left_padding
      let left = repeat_char_to_width(char, left_padding)
      let right = repeat_char_to_width(char, right_padding)
      left <> text <> right
    }
  }
}

/// Helper: repeat a character to fill a target width
fn repeat_char_to_width(char: String, target_width: Int) -> String {
  let char_width = display_width(char)
  case char_width <= 0 {
    True -> ""
    False -> {
      let count = target_width / char_width
      string.repeat(char, count)
    }
  }
}

/// Align text within a given width using the specified alignment.
///
/// This is a convenience function combining pad_left, pad_right, and pad_center.
///
/// ## Examples
///
/// ```gleam
/// align("hi", 5, Left, " ")   // -> "hi   "
/// align("hi", 5, Right, " ")  // -> "   hi"
/// align("hi", 5, Center, " ") // -> " hi  "
/// ```
pub fn align(text: String, width: Int, alignment: Alignment, char: String) -> String {
  case alignment {
    Left -> pad_right(text, width, char)
    Right -> pad_left(text, width, char)
    Center -> pad_center(text, width, char)
  }
}

/// Fit text into exactly the specified width.
///
/// If text is too long, truncates with ellipsis.
/// If text is too short, pads according to alignment.
///
/// ## Examples
///
/// ```gleam
/// fit("hello world", 8, Left, "...", " ")  // -> "hello..."
/// fit("hi", 8, Left, "...", " ")  // -> "hi      "
/// fit("hi", 8, Right, "...", " ") // -> "      hi"
/// ```
pub fn fit(
  text: String,
  width: Int,
  alignment: Alignment,
  ellipsis: String,
  pad_char: String,
) -> String {
  let text_width = display_width(text)
  case int.compare(text_width, width) {
    order.Lt -> align(text, width, alignment, pad_char)
    order.Eq -> text
    order.Gt -> truncate(text, width, ellipsis)
  }
}
