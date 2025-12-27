// Reusable text field with cursor and editing support
//
// Features:
// - Cursor position tracking
// - Character insertion/deletion
// - Word navigation (Alt+Left/Right)
// - Word deletion (Alt+Backspace)
// - Line operations (Ctrl+U clear, Ctrl+A home, Ctrl+E end)
// - Clipboard-style operations (Ctrl+K kill to end)

import gleam/int
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/string

/// Text field state with cursor position
pub type TextField {
  TextField(
    /// The text content
    text: String,
    /// Cursor position (0 = before first char, len = after last char)
    cursor: Int,
  )
}

/// Create an empty text field
pub fn new() -> TextField {
  TextField(text: "", cursor: 0)
}

/// Create a text field with initial text (cursor at end)
pub fn from_string(text: String) -> TextField {
  TextField(text: text, cursor: string.length(text))
}

/// Get the text content
pub fn get_text(field: TextField) -> String {
  field.text
}

/// Get cursor position
pub fn get_cursor(field: TextField) -> Int {
  field.cursor
}

/// Check if field is empty
pub fn is_empty(field: TextField) -> Bool {
  field.text == ""
}

// =============================================================================
// Basic editing operations
// =============================================================================

/// Insert a character at cursor position
pub fn insert(field: TextField, char: String) -> TextField {
  let before = string.slice(field.text, 0, field.cursor)
  let after = string.slice(field.text, field.cursor, string.length(field.text))
  let new_text = before <> char <> after
  let char_len = string.length(char)
  TextField(text: new_text, cursor: field.cursor + char_len)
}

/// Delete character before cursor (backspace)
pub fn backspace(field: TextField) -> TextField {
  case field.cursor {
    0 -> field
    n -> {
      let before = string.slice(field.text, 0, n - 1)
      let after = string.slice(field.text, n, string.length(field.text))
      TextField(text: before <> after, cursor: n - 1)
    }
  }
}

/// Delete character at cursor (delete key)
pub fn delete(field: TextField) -> TextField {
  let len = string.length(field.text)
  case field.cursor >= len {
    True -> field
    False -> {
      let before = string.slice(field.text, 0, field.cursor)
      let after = string.slice(field.text, field.cursor + 1, len)
      TextField(text: before <> after, cursor: field.cursor)
    }
  }
}

/// Clear all text
pub fn clear(field: TextField) -> TextField {
  let _ = field
  TextField(text: "", cursor: 0)
}

// =============================================================================
// Cursor movement
// =============================================================================

/// Move cursor left one character
pub fn move_left(field: TextField) -> TextField {
  case field.cursor {
    0 -> field
    n -> TextField(..field, cursor: n - 1)
  }
}

/// Move cursor right one character
pub fn move_right(field: TextField) -> TextField {
  let len = string.length(field.text)
  case field.cursor >= len {
    True -> field
    False -> TextField(..field, cursor: field.cursor + 1)
  }
}

/// Move cursor to start of text
pub fn move_home(field: TextField) -> TextField {
  TextField(..field, cursor: 0)
}

/// Move cursor to end of text
pub fn move_end(field: TextField) -> TextField {
  TextField(..field, cursor: string.length(field.text))
}

// =============================================================================
// Word operations
// =============================================================================

/// Move cursor left one word (Alt+Left)
pub fn move_word_left(field: TextField) -> TextField {
  let new_cursor = find_word_boundary_left(field.text, field.cursor)
  TextField(..field, cursor: new_cursor)
}

/// Move cursor right one word (Alt+Right)
pub fn move_word_right(field: TextField) -> TextField {
  let new_cursor = find_word_boundary_right(field.text, field.cursor)
  TextField(..field, cursor: new_cursor)
}

/// Delete word before cursor (Alt+Backspace)
pub fn delete_word_back(field: TextField) -> TextField {
  let word_start = find_word_boundary_left(field.text, field.cursor)
  let before = string.slice(field.text, 0, word_start)
  let after = string.slice(field.text, field.cursor, string.length(field.text))
  TextField(text: before <> after, cursor: word_start)
}

/// Delete word after cursor (Alt+Delete or Alt+D)
pub fn delete_word_forward(field: TextField) -> TextField {
  let word_end = find_word_boundary_right(field.text, field.cursor)
  let before = string.slice(field.text, 0, field.cursor)
  let after = string.slice(field.text, word_end, string.length(field.text))
  TextField(text: before <> after, cursor: field.cursor)
}

// =============================================================================
// Line operations
// =============================================================================

/// Clear from cursor to end of line (Ctrl+K)
pub fn kill_to_end(field: TextField) -> TextField {
  let before = string.slice(field.text, 0, field.cursor)
  TextField(text: before, cursor: field.cursor)
}

/// Clear from start to cursor (Ctrl+U)
pub fn kill_to_start(field: TextField) -> TextField {
  let after = string.slice(field.text, field.cursor, string.length(field.text))
  TextField(text: after, cursor: 0)
}

// =============================================================================
// Word boundary helpers
// =============================================================================

/// Find the start of the previous word
fn find_word_boundary_left(text: String, cursor: Int) -> Int {
  case cursor {
    0 -> 0
    _ -> {
      // Convert to graphemes for proper iteration
      let graphemes = string.to_graphemes(text)

      // Skip any spaces immediately before cursor
      let pos = skip_spaces_left(graphemes, cursor - 1)

      // Now find the start of the word
      find_word_start(graphemes, pos)
    }
  }
}

/// Find the end of the next word
fn find_word_boundary_right(text: String, cursor: Int) -> Int {
  let len = string.length(text)
  case cursor >= len {
    True -> len
    False -> {
      let graphemes = string.to_graphemes(text)

      // Skip any spaces at cursor
      let pos = skip_spaces_right(graphemes, cursor)

      // Now find the end of the word
      find_word_end(graphemes, pos)
    }
  }
}

/// Skip spaces going left, return new position
fn skip_spaces_left(graphemes: List(String), pos: Int) -> Int {
  case pos < 0 {
    True -> 0
    False -> {
      case list_get(graphemes, pos) {
        Some(char) -> {
          case is_whitespace(char) {
            True -> skip_spaces_left(graphemes, pos - 1)
            False -> pos
          }
        }
        None -> pos
      }
    }
  }
}

/// Skip spaces going right, return new position
fn skip_spaces_right(graphemes: List(String), pos: Int) -> Int {
  let len = list.length(graphemes)
  case pos >= len {
    True -> len
    False -> {
      case list_get(graphemes, pos) {
        Some(char) -> {
          case is_whitespace(char) {
            True -> skip_spaces_right(graphemes, pos + 1)
            False -> pos
          }
        }
        None -> pos
      }
    }
  }
}

/// Find start of word (first non-word char going left)
fn find_word_start(graphemes: List(String), pos: Int) -> Int {
  case pos <= 0 {
    True -> 0
    False -> {
      case list_get(graphemes, pos - 1) {
        Some(char) -> {
          case is_word_char(char) {
            True -> find_word_start(graphemes, pos - 1)
            False -> pos
          }
        }
        None -> pos
      }
    }
  }
}

/// Find end of word (first non-word char going right)
fn find_word_end(graphemes: List(String), pos: Int) -> Int {
  let len = list.length(graphemes)
  case pos >= len {
    True -> len
    False -> {
      case list_get(graphemes, pos) {
        Some(char) -> {
          case is_word_char(char) {
            True -> find_word_end(graphemes, pos + 1)
            False -> pos
          }
        }
        None -> pos
      }
    }
  }
}

/// Get element at index from list
fn list_get(items: List(a), index: Int) -> Option(a) {
  case index < 0 {
    True -> None
    False -> {
      case list.drop(items, index) |> list.first {
        Ok(item) -> Some(item)
        Error(_) -> None
      }
    }
  }
}

/// Check if character is whitespace
fn is_whitespace(char: String) -> Bool {
  char == " " || char == "\t" || char == "\n" || char == "\r"
}

/// Check if character is a word character (not whitespace or punctuation)
fn is_word_char(char: String) -> Bool {
  case char {
    " " | "\t" | "\n" | "\r" -> False
    "." | "," | ";" | ":" | "!" | "?" -> False
    "(" | ")" | "[" | "]" | "{" | "}" -> False
    "<" | ">" | "/" | "\\" | "|" -> False
    "'" | "\"" | "`" -> False
    "-" | "_" -> False
    "@" | "#" | "$" | "%" | "^" | "&" | "*" | "+" | "=" -> False
    _ -> True
  }
}

// =============================================================================
// Key event handling
// =============================================================================

/// Result of handling a key event
pub type KeyResult {
  /// Field was updated
  Updated(TextField)
  /// Submit was triggered (Enter pressed)
  Submit(TextField)
  /// Cancel was triggered (Escape pressed)
  Cancel
  /// Key was not handled
  Ignored
}

/// Key modifiers
pub type Modifiers {
  Modifiers(ctrl: Bool, alt: Bool, shift: Bool)
}

/// No modifiers
pub fn no_mods() -> Modifiers {
  Modifiers(ctrl: False, alt: False, shift: False)
}

/// Handle a key event and return the result
pub fn handle_key(
  field: TextField,
  key: String,
  mods: Modifiers,
) -> KeyResult {
  case key, mods.ctrl, mods.alt {
    // Submit
    "enter", _, _ | "return", _, _ -> Submit(field)

    // Cancel
    "escape", _, _ -> Cancel

    // Navigation with Ctrl
    "a", True, False -> Updated(move_home(field))
    "e", True, False -> Updated(move_end(field))

    // Line editing with Ctrl
    "u", True, False -> Updated(kill_to_start(field))
    "k", True, False -> Updated(kill_to_end(field))
    "w", True, False -> Updated(delete_word_back(field))

    // Word navigation with Alt
    "left", False, True -> Updated(move_word_left(field))
    "right", False, True -> Updated(move_word_right(field))
    "b", False, True -> Updated(move_word_left(field))
    "f", False, True -> Updated(move_word_right(field))

    // Word deletion with Alt
    "backspace", False, True -> Updated(delete_word_back(field))
    "delete", False, True -> Updated(delete_word_forward(field))
    "d", False, True -> Updated(delete_word_forward(field))

    // Basic navigation
    "left", False, False -> Updated(move_left(field))
    "right", False, False -> Updated(move_right(field))
    "home", _, _ -> Updated(move_home(field))
    "end", _, _ -> Updated(move_end(field))

    // Deletion
    "backspace", False, False -> Updated(backspace(field))
    "delete", False, False -> Updated(delete(field))

    // Character input (single char, no modifiers that would make it a command)
    char, False, False -> {
      case string.length(char) {
        1 -> Updated(insert(field, char))
        _ -> Ignored
      }
    }

    // Unhandled
    _, _, _ -> Ignored
  }
}

// =============================================================================
// Display helpers
// =============================================================================

/// Get the text with a cursor indicator for display
/// Returns (text_before_cursor, cursor_char, text_after_cursor)
pub fn split_at_cursor(field: TextField) -> #(String, Option(String), String) {
  let len = string.length(field.text)
  let before = string.slice(field.text, 0, field.cursor)

  case field.cursor >= len {
    True -> #(before, None, "")
    False -> {
      let char_at = string.slice(field.text, field.cursor, field.cursor + 1)
      let after = string.slice(field.text, field.cursor + 1, len)
      #(before, Some(char_at), after)
    }
  }
}

/// Get display text with visible cursor (using block character or underscore)
pub fn to_display_string(field: TextField, cursor_char: String) -> String {
  case split_at_cursor(field) {
    #(before, Some(char), after) -> before <> char <> after
    #(before, None, _) -> before <> cursor_char
  }
}
