// Port Detector Service
// Polls tmux pane output to detect running server port
// Matches TypeScript pollForPort logic

import gleam/erlang/process.{type Subject}
import gleam/int
import gleam/option.{type Option, None, Some}
import gleam/otp/actor
import gleam/regexp
import gleam/result
import gleam/string
import azedarach/services/tmux

/// Default port detection pattern (matches localhost:PORT or 127.0.0.1:PORT)
pub const default_port_pattern = "localhost:(\\d+)|127\\.0\\.0\\.1:(\\d+)"

/// Port detection polling interval (ms)
const poll_interval_ms = 500

/// Port detection timeout (ms)
const detection_timeout_ms = 30_000

/// Port detector state
pub type DetectorState {
  DetectorState(
    self: Option(Subject(Msg)),
    target: String,
    pattern: regexp.Regexp,
    elapsed_ms: Int,
    callback: Subject(DetectorResult),
  )
}

/// Messages for detector
pub type Msg {
  Init(Subject(Msg))
  Poll
  Stop
}

/// Result sent to callback
pub type DetectorResult {
  PortDetected(Int)
  DetectionTimedOut
  DetectionFailed(String)
}

/// Configuration for starting detector
pub type DetectorConfig {
  DetectorConfig(
    target: String,
    pattern: String,
    callback: Subject(DetectorResult),
  )
}

/// Start a port detector that polls tmux output
pub fn start(config: DetectorConfig) -> Result(Subject(Msg), actor.StartError) {
  case compile_pattern(config.pattern) {
    Ok(compiled) -> {
      let initial_state =
        DetectorState(
          self: None,
          target: config.target,
          pattern: compiled,
          elapsed_ms: 0,
          callback: config.callback,
        )

      actor.new(initial_state)
      |> actor.on_message(handle_message)
      |> actor.start
      |> result.map(fn(started) {
        // Send Init message to set self reference and start polling
        process.send(started.data, Init(started.data))
        started.data
      })
    }
    Error(err) -> {
      // Send failure immediately
      process.send(config.callback, DetectionFailed("Invalid pattern: " <> err))
      // Return an error - pattern compilation failed
      Error(actor.InitTimeout)
    }
  }
}

/// Stop the detector
pub fn stop(detector: Subject(Msg)) -> Nil {
  process.send(detector, Stop)
}

/// Create detector with default pattern
pub fn start_with_defaults(
  target: String,
  callback: Subject(DetectorResult),
) -> Result(Subject(Msg), actor.StartError) {
  start(DetectorConfig(
    target: target,
    pattern: default_port_pattern,
    callback: callback,
  ))
}

/// Main message handler
fn handle_message(
  state: DetectorState,
  msg: Msg,
) -> actor.Next(DetectorState, Msg) {
  case msg {
    Init(self) -> {
      schedule_poll(self, poll_interval_ms)
      actor.continue(DetectorState(..state, self: Some(self)))
    }
    Poll -> handle_poll(state)
    Stop -> actor.stop()
  }
}

/// Handle poll - check tmux output for port
fn handle_poll(state: DetectorState) -> actor.Next(DetectorState, Msg) {
  // Check timeout first
  case state.elapsed_ms >= detection_timeout_ms {
    True -> {
      process.send(state.callback, DetectionTimedOut)
      actor.stop()
    }
    False -> {
      // Capture pane output
      case tmux.capture_pane(state.target, 100) {
        Ok(output) -> {
          case detect_port_in_output(output, state.pattern) {
            Some(port) -> {
              process.send(state.callback, PortDetected(port))
              actor.stop()
            }
            None -> {
              // Keep polling
              case state.self {
                Some(self) -> schedule_poll(self, poll_interval_ms)
                None -> Nil
              }
              actor.continue(DetectorState(
                ..state,
                elapsed_ms: state.elapsed_ms + poll_interval_ms,
              ))
            }
          }
        }
        Error(_) -> {
          // Tmux error, keep trying
          case state.self {
            Some(self) -> schedule_poll(self, poll_interval_ms)
            None -> Nil
          }
          actor.continue(DetectorState(
            ..state,
            elapsed_ms: state.elapsed_ms + poll_interval_ms,
          ))
        }
      }
    }
  }
}

/// Detect port in output using regex pattern
fn detect_port_in_output(output: String, pattern: regexp.Regexp) -> Option(Int) {
  case regexp.scan(pattern, output) {
    [] -> None
    [match, ..] -> {
      // Try each subgroup to find the port number
      find_port_in_submatches(match.submatches)
    }
  }
}

/// Find port number in regex submatches
fn find_port_in_submatches(submatches: List(Option(String))) -> Option(Int) {
  case submatches {
    [] -> None
    [Some(s), ..rest] -> {
      case int.parse(s) {
        Ok(port) if port > 0 && port < 65536 -> Some(port)
        _ -> find_port_in_submatches(rest)
      }
    }
    [None, ..rest] -> find_port_in_submatches(rest)
  }
}

/// Compile regex pattern
fn compile_pattern(pattern: String) -> Result(regexp.Regexp, String) {
  regexp.from_string(pattern)
  |> result.map_error(fn(e) { string.inspect(e) })
}

/// Make default pattern
fn make_default_pattern() -> regexp.Regexp {
  case regexp.from_string(default_port_pattern) {
    Ok(r) -> r
    Error(_) -> panic as "Default port pattern should always compile"
  }
}

/// Schedule next poll by spawning a process that sends Poll after delay
fn schedule_poll(self: Subject(Msg), delay_ms: Int) -> Nil {
  let _ =
    process.spawn(fn() {
      process.sleep(delay_ms)
      process.send(self, Poll)
    })
  Nil
}

/// Manually detect port once (for testing or one-shot detection)
pub fn detect_once(
  target: String,
  pattern_opt: Option(String),
) -> Result(Option(Int), String) {
  let pattern_str = option.unwrap(pattern_opt, default_port_pattern)

  case compile_pattern(pattern_str) {
    Error(e) -> Error("Invalid pattern: " <> e)
    Ok(pattern) -> {
      case tmux.capture_pane(target, 100) {
        Ok(output) -> Ok(detect_port_in_output(output, pattern))
        Error(e) -> Error(tmux.error_to_string(e))
      }
    }
  }
}
