// Status bar - single line at bottom with mode and key hints
// Compact format to fit terminal width
//
// NOTE: Shore's Row divides width equally among children, which breaks inline text.
// We use string concatenation to build single text nodes for proper single-line display.

import gleam/dict
import gleam/int
import gleam/list
import gleam/option.{None, Some}
import gleam/string
import azedarach/domain/session
import azedarach/ui/model.{type Model, Normal, Select}
import azedarach/ui/view/utils.{type Node, text}

pub fn render(model: Model) -> Node {
  let #(width, _height) = model.terminal_size

  // Build status bar as concatenated strings (Shore Row doesn't do inline text)
  let left = render_left(model)
  let keybinds = render_keybinds(model, width)
  let right = render_right(model)

  // Single text node for proper single-line display
  text(left <> "  " <> keybinds <> "  " <> right)
}

fn render_left(model: Model) -> String {
  // Project name (short)
  let project = case model.current_project {
    Some(p) -> get_short_name(p)
    None -> "az"
  }

  // Connection indicator
  let conn_icon = "●"

  // Mode (3 chars)
  let mode = get_mode_label(model)

  // View mode indicator
  let view_indicator = "KAN"

  project <> " " <> conn_icon <> " " <> mode <> " " <> view_indicator
}

fn get_short_name(path: String) -> String {
  path
  |> string.split("/")
  |> list.last
  |> option.from_result
  |> option.map(fn(s) { string.slice(s, 0, 10) })
  |> option.unwrap("proj")
}

fn get_mode_label(model: Model) -> String {
  // Check overlays first
  case model.overlay {
    Some(model.ActionMenu) -> "ACT"
    Some(model.SortMenu) -> "SRT"
    Some(model.FilterMenu) -> "FLT"
    Some(_) -> "OVL"
    None -> {
      case model.input {
        Some(model.SearchInput(_)) -> "SRC"
        Some(_) -> "INP"
        None -> {
          case model.pending_key {
            Some("g") -> "GTO"
            Some(_) -> "..."
            None -> {
              case model.mode {
                Normal -> "NOR"
                Select(_) -> "SEL"
              }
            }
          }
        }
      }
    }
  }
}

fn render_keybinds(model: Model, width: Int) -> String {
  let hints = get_mode_keybinds(model)

  // Calculate available width for keybinds
  // Reserve ~40 chars for left (project+mode+view) and right (stats)
  let available = width - 45

  // Build keybind string, stopping when too long
  build_keybind_string(hints, available, "")
}

fn build_keybind_string(
  hints: List(#(String, String)),
  remaining: Int,
  acc: String,
) -> String {
  case hints {
    [] -> acc
    [#(key, action), ..rest] -> {
      let hint = key <> " " <> action
      let hint_len = string.length(hint) + 2  // +2 for spacing

      case hint_len > remaining {
        True -> acc <> " ? more"  // Show indicator that there are more
        False -> {
          let new_acc = case acc {
            "" -> hint
            _ -> acc <> "  " <> hint
          }
          build_keybind_string(rest, remaining - hint_len, new_acc)
        }
      }
    }
  }
}

/// Get keybindings for current mode as (key, action) tuples
/// Matches the Bun/TypeScript version for full feature parity
fn get_mode_keybinds(model: Model) -> List(#(String, String)) {
  case model.overlay {
    Some(model.ActionMenu) -> [
      #("h/l", "Move"),
      #("s", "Start"),
      #("a", "Attach"),
      #("A", "Inline"),
      #("p", "Pause"),
      #("r", "Dev"),
      #("R", "Resume"),
      #("x", "Stop"),
      #("e", "Edit"),
      #("P", "PR"),
      #("d", "Delete"),
      #("Esc", "Cancel"),
    ]
    Some(model.SortMenu) -> [
      #("s", "Session"),
      #("p", "Priority"),
      #("u", "Updated"),
      #("Esc", "Cancel"),
    ]
    Some(model.FilterMenu) -> [
      #("s", "Status"),
      #("p", "Priority"),
      #("t", "Type"),
      #("S", "Session"),
      #("e", "Epic"),
      #("c", "Clear"),
      #("0-4", "P0-P4"),
      #("Esc", "Cancel"),
    ]
    Some(_) -> [#("Esc", "Close"), #("q", "Close")]
    None -> {
      case model.input {
        Some(model.SearchInput(_)) -> [#("Enter", "Confirm"), #("Esc", "Clear")]
        Some(_) -> [#("Enter", "Send"), #("Esc", "Cancel")]
        None -> {
          case model.pending_key {
            Some("g") -> [
              #("w", "Jump"),
              #("g", "First"),
              #("e", "Last"),
              #("h", "Left"),
              #("l", "Right"),
              #("Esc", "Cancel"),
            ]
            Some(_) -> []
            None -> {
              case model.mode {
                Normal -> [
                  #("Space", "Menu"),
                  #(",", "Sort"),
                  #("/", "Search"),
                  #("v", "Select"),
                  #("g", "Goto"),
                  #("hjkl", "Nav"),
                  #("Enter", "Details"),
                  #("c", "Create"),
                  #("Tab", "View"),
                  #("r", "Refresh"),
                  #("a", "VC"),
                  #(":", "Cmd"),
                  #("C-d/u", "Page"),
                  #("?", "more"),
                ]
                Select(_) -> [
                  #("Space", "Toggle"),
                  #("hjkl", "Nav"),
                  #("v", "Exit"),
                  #("Esc", "Clear"),
                ]
              }
            }
          }
        }
      }
    }
  }
}

fn render_right(model: Model) -> String {
  // Count sessions
  let busy =
    dict.values(model.sessions)
    |> list.filter(fn(s) { s.state == session.Busy })
    |> list.length

  let waiting =
    dict.values(model.sessions)
    |> list.filter(fn(s) { s.state == session.Waiting })
    |> list.length

  // Format: "Tasks: N  Active: N"
  let task_count = list.length(model.tasks)
  let session_count = busy + waiting

  let session_str = case session_count > 0 {
    True -> "  Active: " <> int.to_string(session_count)
    False -> ""
  }

  "Tasks: " <> int.to_string(task_count) <> session_str
}
