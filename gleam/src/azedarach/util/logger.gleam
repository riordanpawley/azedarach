// Simple file-based logger for debugging
// Writes to /tmp/azedarach.log

import gleam/list
import gleam/string
import simplifile

const log_path = "/tmp/azedarach.log"

pub type LogLevel {
  Debug
  Info
  Warn
  Error
}

fn level_to_string(level: LogLevel) -> String {
  case level {
    Debug -> "DEBUG"
    Info -> "INFO"
    Warn -> "WARN"
    Error -> "ERROR"
  }
}

/// Initialize the log file (clears previous contents)
pub fn init() -> Nil {
  let _ = simplifile.write(log_path, "=== Azedarach Log Started ===\n")
  Nil
}

/// Log a message at the specified level
pub fn log(level: LogLevel, message: String) -> Nil {
  let line = "[" <> level_to_string(level) <> "] " <> message <> "\n"
  let _ = simplifile.append(log_path, line)
  Nil
}

/// Log debug message
pub fn debug(message: String) -> Nil {
  log(Debug, message)
}

/// Log info message
pub fn info(message: String) -> Nil {
  log(Info, message)
}

/// Log warning message
pub fn warn(message: String) -> Nil {
  log(Warn, message)
}

/// Log error message
pub fn error(message: String) -> Nil {
  log(Error, message)
}

/// Log with context (key-value pairs)
pub fn log_ctx(level: LogLevel, message: String, context: List(#(String, String))) -> Nil {
  let ctx_str = context
    |> list.map(fn(pair) { pair.0 <> "=" <> pair.1 })
    |> string.join(", ")
  let full_msg = case ctx_str {
    "" -> message
    _ -> message <> " | " <> ctx_str
  }
  log(level, full_msg)
}

/// Debug with context
pub fn debug_ctx(message: String, context: List(#(String, String))) -> Nil {
  log_ctx(Debug, message, context)
}

/// Info with context
pub fn info_ctx(message: String, context: List(#(String, String))) -> Nil {
  log_ctx(Info, message, context)
}

/// Error with context
pub fn error_ctx(message: String, context: List(#(String, String))) -> Nil {
  log_ctx(Error, message, context)
}
