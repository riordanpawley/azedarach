//// Input handling for LustreTUI - wraps Etch's event handling.
////
//// This module provides a thin wrapper around Etch's input handling, providing:
//// - Terminal initialization (raw mode, alt screen)
//// - Event polling and reading
//// - Convenient helper functions for checking key events and modifiers
////
//// The actual input parsing (ANSI escape sequences, etc.) is handled by Etch.
////
//// ## Usage
////
//// ```gleam
//// import tui_core/input
//// import etch/event.{Key, Char}
//// import gleam/option.{None, Some}
////
//// pub fn main() {
////   // Initialize terminal and event server
////   input.init()
////
////   // Main event loop
////   loop()
//// }
////
//// fn loop() {
////   case input.poll(100) {
////     Some(Ok(Key(key_event))) -> {
////       case key_event.code {
////         Char("q") -> input.cleanup()
////         _ -> loop()
////       }
////     }
////     _ -> loop()
////   }
//// }
//// ```

import etch/command
import etch/event
import etch/stdout
import etch/terminal
import gleam/option.{type Option, None, Some}

// ============================================================================
// Re-export Etch types for convenience
// ============================================================================

/// Event from terminal - can be key press, mouse, resize, focus, or paste
pub type Event =
  event.Event

/// Key event containing the pressed key and modifiers
pub type KeyEvent =
  event.KeyEvent

/// Mouse event (if mouse support is enabled)
pub type MouseEvent =
  event.MouseEvent

/// Key code representing which key was pressed
pub type KeyCode =
  event.KeyCode

/// Keyboard modifiers (shift, ctrl, alt, super, hyper, meta)
pub type Modifiers =
  event.Modifiers

/// Event error returned when parsing fails
pub type EventError =
  event.EventError

// Note: To pattern match on events, import etch/event directly:
//
// ```gleam
// import etch/event.{Key, Resize, Char, Esc}
//
// case input.read() {
//   Some(Ok(Key(key_event))) -> ...
//   Some(Ok(Resize(w, h))) -> ...
// }
// ```

// ============================================================================
// Terminal initialization and cleanup
// ============================================================================

/// Initialize terminal for TUI mode.
///
/// This enters raw mode, switches to alternate screen, hides cursor,
/// and starts the event server for input handling.
/// Call `cleanup()` to restore terminal on exit.
///
/// ## Example
///
/// ```gleam
/// input.init()
/// // Run TUI application...
/// input.cleanup()
/// ```
pub fn init() -> Nil {
  stdout.execute([
    command.EnterRaw,
    command.EnterAlternateScreen,
    command.HideCursor,
  ])
  let _ = event.init_event_server()
  Nil
}

/// Initialize terminal with mouse support enabled.
///
/// Same as `init()` but also enables mouse event capturing.
pub fn init_with_mouse() -> Nil {
  stdout.execute([
    command.EnableMouseCapture,
    command.EnterRaw,
    command.EnterAlternateScreen,
    command.HideCursor,
  ])
  let _ = event.init_event_server()
  Nil
}

/// Restore terminal to normal mode.
///
/// This shows cursor, exits alternate screen, and exits raw mode.
/// Always call this before exiting, even on errors.
pub fn cleanup() -> Nil {
  stdout.execute([
    command.ShowCursor,
    command.LeaveAlternateScreen,
  ])
  terminal.exit_raw()
}

/// Restore terminal with mouse cleanup.
///
/// Same as `cleanup()` but also disables mouse event capturing.
pub fn cleanup_with_mouse() -> Nil {
  stdout.execute([
    command.DisableMouseCapture,
    command.ShowCursor,
    command.LeaveAlternateScreen,
  ])
  terminal.exit_raw()
}

// ============================================================================
// Event polling and reading
// ============================================================================

/// Poll for an input event with timeout.
///
/// Returns `Some(Ok(event))` if an event occurs within the timeout,
/// `Some(Error(..))` if parsing failed, or `None` if the timeout expires.
///
/// This is non-blocking and ideal for event loops that need to
/// do other work while waiting for input.
///
/// ## Example
///
/// ```gleam
/// case poll(100) {
///   Some(Ok(Key(key_event))) -> handle_key(key_event)
///   Some(Ok(Resize(w, h))) -> handle_resize(w, h)
///   Some(Ok(_)) -> Nil
///   Some(Error(_)) -> Nil  // Parse error
///   None -> {
///     // No input, do other work
///     update_display()
///   }
/// }
/// ```
pub fn poll(timeout_ms: Int) -> Option(Result(Event, EventError)) {
  event.poll(timeout_ms)
}

/// Read the next event, blocking until one is available.
///
/// Returns `Some(Ok(event))` when an event is received,
/// `Some(Error(..))` if parsing failed, or `None` if interrupted.
///
/// This blocks the current process until an event occurs.
/// Use `poll()` for non-blocking behavior.
///
/// ## Example
///
/// ```gleam
/// case read() {
///   Some(Ok(Key(key_event))) -> handle_key(key_event)
///   Some(Ok(Resize(w, h))) -> handle_resize(w, h)
///   Some(Ok(_)) -> Nil
///   Some(Error(_)) -> Nil
///   None -> Nil
/// }
/// ```
pub fn read() -> Option(Result(Event, EventError)) {
  event.read()
}

// ============================================================================
// Key event helpers
// ============================================================================

/// Check if an event is a key press matching a specific key code.
///
/// ## Example
///
/// ```gleam
/// case is_key(evt, Esc) {
///   True -> quit()
///   False -> continue()
/// }
/// ```
pub fn is_key(evt: Event, key: KeyCode) -> Bool {
  case evt {
    event.Key(key_event) -> key_event.code == key
    _ -> False
  }
}

/// Check if a key event has the Ctrl modifier.
pub fn has_ctrl(key_event: KeyEvent) -> Bool {
  key_event.modifiers.control
}

/// Check if a key event has the Alt modifier.
pub fn has_alt(key_event: KeyEvent) -> Bool {
  key_event.modifiers.alt
}

/// Check if a key event has the Shift modifier.
pub fn has_shift(key_event: KeyEvent) -> Bool {
  key_event.modifiers.shift
}

/// Check if a key event has the Super modifier (Windows/Command key).
pub fn has_super(key_event: KeyEvent) -> Bool {
  key_event.modifiers.super
}

/// Check if a key event has any modifier pressed.
pub fn has_any_modifier(key_event: KeyEvent) -> Bool {
  let m = key_event.modifiers
  m.control || m.alt || m.shift || m.super || m.hyper || m.meta
}

/// Get the character from a Char key event, if applicable.
///
/// Returns `Some(char)` for character keys, `None` for special keys.
///
/// ## Example
///
/// ```gleam
/// case get_char(key_event) {
///   Some("q") -> quit()
///   Some(c) -> insert_char(c)
///   None -> Nil  // Special key like arrows, function keys
/// }
/// ```
pub fn get_char(key_event: KeyEvent) -> Option(String) {
  case key_event.code {
    event.Char(c) -> Some(c)
    _ -> None
  }
}

// ============================================================================
// Event pattern matching helpers
// ============================================================================

/// Extract key event from an Event if it's a key press.
pub fn as_key(evt: Event) -> Option(KeyEvent) {
  case evt {
    event.Key(k) -> Some(k)
    _ -> None
  }
}

/// Extract mouse event from an Event if it's a mouse event.
pub fn as_mouse(evt: Event) -> Option(MouseEvent) {
  case evt {
    event.Mouse(m) -> Some(m)
    _ -> None
  }
}

/// Extract resize dimensions from an Event if it's a resize event.
pub fn as_resize(evt: Event) -> Option(#(Int, Int)) {
  case evt {
    event.Resize(w, h) -> Some(#(w, h))
    _ -> None
  }
}

/// Check if an event is a key event.
pub fn is_key_event(evt: Event) -> Bool {
  case evt {
    event.Key(_) -> True
    _ -> False
  }
}

/// Check if an event is a mouse event.
pub fn is_mouse_event(evt: Event) -> Bool {
  case evt {
    event.Mouse(_) -> True
    _ -> False
  }
}

/// Check if an event is a resize event.
pub fn is_resize_event(evt: Event) -> Bool {
  case evt {
    event.Resize(_, _) -> True
    _ -> False
  }
}

// ============================================================================
// Terminal size
// ============================================================================

/// Get the current terminal size.
///
/// Returns a tuple of (columns, rows) in character cells.
pub fn terminal_size() -> #(Int, Int) {
  terminal.window_size()
}
