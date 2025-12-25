// Hook Configuration for Azedarach Session State Detection
//
// Generates and manages Claude Code hook configuration that enables
// session state detection via `az-notify.sh` commands.
//
// This module mirrors the TypeScript src/core/hooks.ts implementation.

import gleam/option.{type Option, None, Some}
import gleam/result
import gleam/string
import azedarach/util/shell

// ============================================================================
// Path Resolution
// ============================================================================

/// Get the path to az-notify.sh
///
/// Resolution order:
/// 1. AZEDARACH_HOME environment variable + /bin/az-notify.sh
/// 2. Relative to project root (for development)
pub fn get_notify_path() -> Result(String, Nil) {
  // Try AZEDARACH_HOME first
  case shell.get_env("AZEDARACH_HOME") {
    Ok(home) -> Ok(home <> "/bin/az-notify.sh")
    Error(_) -> {
      // Try to find relative to cwd (development mode)
      case shell.cwd() {
        Ok(cwd) -> {
          let dev_path = cwd <> "/bin/az-notify.sh"
          case shell.path_exists(dev_path) {
            True -> Ok(dev_path)
            False -> {
              // Try parent directory (if in gleam/ subdir)
              let parent_path = shell.dirname(cwd) <> "/bin/az-notify.sh"
              case shell.path_exists(parent_path) {
                True -> Ok(parent_path)
                False -> Error(Nil)
              }
            }
          }
        }
        Error(_) -> Error(Nil)
      }
    }
  }
}

// ============================================================================
// Hook Command Building
// ============================================================================

/// Build the az notify command with proper path handling
///
/// Uses the lightweight shell script (az-notify.sh) instead of the full
/// CLI for maximum speed. The shell script directly calls tmux
/// without any compilation overhead (~10ms vs ~600ms).
fn build_notify_command(event: String, bead_id: String, notify_path: String) -> String {
  "\"" <> notify_path <> "\" " <> event <> " " <> bead_id
}

// ============================================================================
// Hook Configuration Generation
// ============================================================================

/// Generate Claude Code hook configuration for session state detection
///
/// Creates hooks that call `az-notify.sh` when Claude enters specific states.
/// This enables authoritative state detection from Claude's native hook system.
///
/// Hook events:
/// - UserPromptSubmit - User sends a prompt (busy detection)
/// - PreToolUse - Claude is about to use a tool (busy detection)
/// - Notification (idle_prompt) - Claude is waiting for user input at the prompt
/// - PermissionRequest - Claude is waiting for permission approval
/// - Stop - Claude session stops (Ctrl+C, completion, etc.)
/// - SessionEnd - Claude session fully ends
pub fn generate_hook_config(bead_id: String, notify_path: String) -> String {
  let user_prompt_cmd = build_notify_command("user_prompt", bead_id, notify_path)
  let pretooluse_cmd = build_notify_command("pretooluse", bead_id, notify_path)
  let idle_prompt_cmd = build_notify_command("idle_prompt", bead_id, notify_path)
  let permission_cmd = build_notify_command("permission_request", bead_id, notify_path)
  let stop_cmd = build_notify_command("stop", bead_id, notify_path)
  let session_end_cmd = build_notify_command("session_end", bead_id, notify_path)

  "{
  \"hooks\": {
    \"UserPromptSubmit\": [
      {
        \"hooks\": [
          {
            \"type\": \"command\",
            \"command\": \"" <> escape_json(user_prompt_cmd) <> "\"
          }
        ]
      }
    ],
    \"PreToolUse\": [
      {
        \"hooks\": [
          {
            \"type\": \"command\",
            \"command\": \"" <> escape_json(pretooluse_cmd) <> "\"
          }
        ]
      }
    ],
    \"Notification\": [
      {
        \"matcher\": \"idle_prompt\",
        \"hooks\": [
          {
            \"type\": \"command\",
            \"command\": \"" <> escape_json(idle_prompt_cmd) <> "\"
          }
        ]
      }
    ],
    \"PermissionRequest\": [
      {
        \"hooks\": [
          {
            \"type\": \"command\",
            \"command\": \"" <> escape_json(permission_cmd) <> "\"
          }
        ]
      }
    ],
    \"Stop\": [
      {
        \"hooks\": [
          {
            \"type\": \"command\",
            \"command\": \"" <> escape_json(stop_cmd) <> "\"
          }
        ]
      }
    ],
    \"SessionEnd\": [
      {
        \"hooks\": [
          {
            \"type\": \"command\",
            \"command\": \"" <> escape_json(session_end_cmd) <> "\"
          }
        ]
      }
    ]
  }
}"
}

/// Generate hook configuration, auto-detecting the notify script path
pub fn generate_hook_config_auto(bead_id: String) -> Result(String, String) {
  case get_notify_path() {
    Ok(path) -> Ok(generate_hook_config(bead_id, path))
    Error(_) -> Error("Could not find az-notify.sh. Set AZEDARACH_HOME environment variable.")
  }
}

// ============================================================================
// Helpers
// ============================================================================

/// Escape a string for JSON
fn escape_json(s: String) -> String {
  s
  |> string.replace("\\", "\\\\")
  |> string.replace("\"", "\\\"")
}
