//// Terminal resize handling for LustreTUI.
////
//// This module provides utilities for handling terminal resize events.
//// Etch already handles SIGWINCH and delivers Resize events - this module
//// provides a convenient API layer on top.
////
//// ## Usage
////
//// ### Method 1: Direct event handling
////
//// ```gleam
//// import tui_core/input
//// import tui_core/resize.{Dimensions}
//// import gleam/option.{None, Some}
////
//// fn handle_event(event: input.Event) {
////   case resize.from_event(event) {
////     Some(dims) -> {
////       // Terminal resized! Relayout with new dimensions
////       relayout(dims.width, dims.height)
////     }
////     None -> {
////       // Handle other events
////       handle_other(event)
////     }
////   }
//// }
//// ```
////
//// ### Method 2: With tracker for polling-based apps
////
//// ```gleam
//// import tui_core/input
//// import tui_core/resize.{type ResizeTracker}
//// import gleam/option.{None, Some}
////
//// fn event_loop(tracker: ResizeTracker) {
////   case input.poll(100) {
////     Some(Ok(event)) -> {
////       let #(new_tracker, resized) = resize.update_tracker(tracker, event)
////       case resized {
////         True -> relayout(new_tracker.last_size)
////         False -> Nil
////       }
////       event_loop(new_tracker)
////     }
////     _ -> event_loop(tracker)
////   }
//// }
//// ```

import etch/event
import gleam/option.{type Option, None, Some}
import tui_core/input

// ============================================================================
// Types
// ============================================================================

/// Terminal dimensions in character cells.
pub type Dimensions {
  Dimensions(width: Int, height: Int)
}

/// Tracker for resize events, useful for polling-based apps.
///
/// Keeps track of the last known terminal size so you can detect
/// when a resize has occurred.
pub type ResizeTracker {
  ResizeTracker(last_size: Dimensions)
}

// ============================================================================
// Dimension functions
// ============================================================================

/// Get current terminal dimensions.
///
/// This queries the terminal for its current size.
///
/// ## Example
///
/// ```gleam
/// let dims = resize.get_size()
/// io.println("Terminal is " <> int.to_string(dims.width) <> "x" <> int.to_string(dims.height))
/// ```
pub fn get_size() -> Dimensions {
  let #(width, height) = input.terminal_size()
  Dimensions(width: width, height: height)
}

/// Check if dimensions have changed.
///
/// Returns `True` if either width or height differs between old and new.
///
/// ## Example
///
/// ```gleam
/// let old = Dimensions(80, 24)
/// let new = Dimensions(120, 24)
/// resize.changed(old, new)  // True - width changed
/// ```
pub fn changed(old: Dimensions, new: Dimensions) -> Bool {
  old.width != new.width || old.height != new.height
}

/// Extract dimensions from a resize event.
///
/// Returns `Some(Dimensions)` if the event is a Resize event,
/// `None` for all other event types.
///
/// ## Example
///
/// ```gleam
/// case resize.from_event(event) {
///   Some(dims) -> relayout(dims.width, dims.height)
///   None -> handle_other(event)
/// }
/// ```
pub fn from_event(event: input.Event) -> Option(Dimensions) {
  case event {
    event.Resize(width, height) -> Some(Dimensions(width: width, height: height))
    _ -> None
  }
}

// ============================================================================
// Resize tracker
// ============================================================================

/// Create a new resize tracker with the current terminal size.
///
/// The tracker stores the last known size so you can detect
/// when the terminal has been resized.
///
/// ## Example
///
/// ```gleam
/// let tracker = resize.new_tracker()
/// event_loop(tracker)
/// ```
pub fn new_tracker() -> ResizeTracker {
  ResizeTracker(last_size: get_size())
}

/// Update the tracker with an event.
///
/// If the event is a Resize event, updates the stored size.
/// Returns a tuple of the (possibly updated) tracker and a boolean
/// indicating whether a resize occurred.
///
/// ## Example
///
/// ```gleam
/// let #(new_tracker, did_resize) = resize.update_tracker(tracker, event)
/// case did_resize {
///   True -> {
///     let dims = new_tracker.last_size
///     relayout(dims.width, dims.height)
///   }
///   False -> Nil
/// }
/// ```
pub fn update_tracker(
  tracker: ResizeTracker,
  event: input.Event,
) -> #(ResizeTracker, Bool) {
  case from_event(event) {
    Some(new_dims) -> {
      let did_resize = changed(tracker.last_size, new_dims)
      #(ResizeTracker(last_size: new_dims), did_resize)
    }
    None -> #(tracker, False)
  }
}

// ============================================================================
// Utility functions
// ============================================================================

/// Convert dimensions to a tuple.
///
/// Useful when interoperating with APIs that expect `#(width, height)`.
pub fn to_tuple(dims: Dimensions) -> #(Int, Int) {
  #(dims.width, dims.height)
}

/// Create dimensions from a tuple.
///
/// Useful when converting from APIs that return `#(width, height)`.
pub fn from_tuple(tuple: #(Int, Int)) -> Dimensions {
  let #(width, height) = tuple
  Dimensions(width: width, height: height)
}

/// Get the area (total character cells) of the dimensions.
pub fn area(dims: Dimensions) -> Int {
  dims.width * dims.height
}

/// Check if dimensions fit within another set of dimensions.
///
/// Returns `True` if `inner` fits completely within `outer`.
pub fn fits_within(inner: Dimensions, outer: Dimensions) -> Bool {
  inner.width <= outer.width && inner.height <= outer.height
}
