// State detector - detect Claude session state from output

import gleam/list
import gleam/regex
import gleam/string
import azedarach/domain/session.{type State}

// Detect state from tmux pane output
pub fn detect(output: String) -> State {
  let lines = string.split(output, "\n")
  let last_lines = list.reverse(lines) |> list.take(10) |> list.reverse

  // Check patterns in order of priority
  case detect_error(last_lines) {
    True -> session.Error
    False -> {
      case detect_waiting(last_lines) {
        True -> session.Waiting
        False -> {
          case detect_done(last_lines) {
            True -> session.Done
            False -> {
              case detect_busy(last_lines) {
                True -> session.Busy
                False -> session.Idle
              }
            }
          }
        }
      }
    }
  }
}

// Detect error patterns
fn detect_error(lines: List(String)) -> Bool {
  let error_patterns = [
    "Error:",
    "Exception:",
    "Failed:",
    "FAILED",
    "error:",
    "panic:",
    "fatal:",
    "✗",
  ]

  list.any(lines, fn(line) {
    list.any(error_patterns, fn(pattern) { string.contains(line, pattern) })
  })
}

// Detect waiting for input patterns
fn detect_waiting(lines: List(String)) -> Bool {
  let waiting_patterns = [
    "[y/n]",
    "[Y/n]",
    "(y/n)",
    "Do you want to",
    "Would you like to",
    "Press Enter",
    "Continue?",
    "Proceed?",
    "approve",
    "permission",
    "> ",
    // Prompt indicator
  ]

  list.any(lines, fn(line) {
    list.any(waiting_patterns, fn(pattern) { string.contains(line, pattern) })
  })
}

// Detect completion patterns
fn detect_done(lines: List(String)) -> Bool {
  let done_patterns = [
    "Task completed",
    "Successfully",
    "Done!",
    "Finished",
    "Complete",
    "All done",
    "✓",
    "✔",
  ]

  list.any(lines, fn(line) {
    list.any(done_patterns, fn(pattern) { string.contains(line, pattern) })
  })
}

// Detect busy patterns (actively working)
fn detect_busy(lines: List(String)) -> Bool {
  let busy_patterns = [
    "Running",
    "Executing",
    "Processing",
    "Loading",
    "Building",
    "Compiling",
    "Testing",
    "...",
    "⟳",
    "●",
  ]

  list.any(lines, fn(line) {
    list.any(busy_patterns, fn(pattern) { string.contains(line, pattern) })
  })
}

// More detailed state with context
pub type DetectedState {
  DetectedState(state: State, context: String)
}

pub fn detect_with_context(output: String) -> DetectedState {
  let state = detect(output)
  let lines = string.split(output, "\n")
  let last_line =
    lines
    |> list.reverse
    |> list.find(fn(l) { l != "" })
    |> result.unwrap("")

  DetectedState(state: state, context: last_line)
}

import gleam/result
