// Kanban board view - 4 columns

import gleam/dict
import gleam/int
import gleam/list
import gleam/option.{None, Some}
import gleam/string
import shore/ui
import azedarach/domain/task.{type Task}
import azedarach/domain/session
import azedarach/ui/model.{type Model}
import azedarach/ui/theme
import azedarach/ui/view/utils.{
  type Node, center, dim_text, hbox, pad_right, styled_text, text, truncate, vbox,
}

const column_names = ["Open", "In Progress", "Blocked", "Closed"]

pub fn render(model: Model) -> Node {
  let #(width, height) = model.terminal_size
  let column_width = width / 4

  // Check if we're in epic drill-down mode
  case model.current_epic {
    Some(_) -> render_with_epic_header(model, width, height, column_width)
    None -> render_board(model, height - 1, column_width)
  }
}

fn render_with_epic_header(
  model: Model,
  width: Int,
  height: Int,
  column_width: Int,
) -> Node {
  let colors = model.colors

  // Get epic info
  let epic_header = case model.get_current_epic(model) {
    Some(epic) -> {
      let #(completed, total) = model.epic_progress(model, epic.id)
      render_epic_header(epic, completed, total, width, colors)
    }
    None -> text("")
  }

  // Board takes remaining height (minus status bar and epic header)
  let board_height = height - 2
  let board = render_board(model, board_height, column_width)

  vbox([epic_header, board])
}

fn render_epic_header(
  epic: Task,
  completed: Int,
  total: Int,
  width: Int,
  colors: theme.Colors,
) -> Node {
  // Back indicator
  let back_hint = "← Backspace"

  // Epic title (truncated)
  let title_max = width - string.length(back_hint) - 30
  let epic_title = "Epic: " <> truncate(epic.title, title_max)

  // Progress bar
  let progress_bar = render_progress_bar(completed, total, 20, colors)

  // Progress text
  let progress_text =
    " " <> int.to_string(completed) <> "/" <> int.to_string(total) <> " "

  hbox([
    styled_text(back_hint, colors.overlay0),
    text(" "),
    styled_text(epic_title, colors.mauve),
    text(" "),
    progress_bar,
    styled_text(progress_text, colors.subtext0),
  ])
}

fn render_progress_bar(
  completed: Int,
  total: Int,
  bar_width: Int,
  colors: theme.Colors,
) -> Node {
  case total {
    0 -> styled_text("[" <> string.repeat("-", bar_width) <> "]", colors.subtext0)
    _ -> {
      let filled = { completed * bar_width } / total
      let empty = bar_width - filled
      let filled_str = string.repeat("█", filled)
      let empty_str = string.repeat("░", empty)
      hbox([
        styled_text("[", colors.subtext0),
        styled_text(filled_str, colors.green),
        styled_text(empty_str, colors.surface1),
        styled_text("]", colors.subtext0),
      ])
    }
  }
}

fn render_board(model: Model, board_height: Int, column_width: Int) -> Node {
  let columns =
    list.index_map(column_names, fn(name, idx) {
      render_column(model, idx, name, column_width, board_height)
    })

  hbox(columns)
}

fn render_column(
  model: Model,
  index: Int,
  name: String,
  width: Int,
  _height: Int,
) -> Node {
  let colors = model.colors
  let sem = theme.semantic(colors)
  let is_selected = model.cursor.column_index == index
  let header_color = theme.column_color(colors, index)

  // Get tasks for this column
  let tasks = model.tasks_in_column(model, index)

  // Column header
  let header = render_header(name, width, header_color, is_selected, sem)

  // Task cards
  let cards =
    list.index_map(tasks, fn(task, task_idx) {
      let is_cursor = is_selected && model.cursor.task_index == task_idx
      let session_state = dict.get(model.sessions, task.id)
      render_card(task, session_state, width - 2, is_cursor, model)
    })

  // Empty state
  let content = case list.is_empty(cards) {
    True -> [dim_text(center("(empty)", width - 2))]
    False -> cards
  }

  // Shore doesn't support hex colors, use plain box
  ui.box_styled([header, ..content], Some(name), None)
}

fn render_header(
  name: String,
  width: Int,
  color: String,
  is_selected: Bool,
  _sem: theme.SemanticColors,
) -> Node {
  let indicator = case is_selected {
    True -> "▶ "
    False -> "  "
  }
  let header_text = indicator <> name
  let padded = pad_right(header_text, width - 2)

  styled_text(padded, color)
}

fn render_card(
  task: Task,
  session_state: Result(session.SessionState, Nil),
  width: Int,
  is_cursor: Bool,
  model: Model,
) -> Node {
  let colors = model.colors
  let sem = theme.semantic(colors)

  // Cursor indicator
  let cursor_prefix = case is_cursor {
    True -> "▸"
    False -> " "
  }

  // Session state indicator
  let state_indicator = case session_state {
    Ok(ss) -> session.state_icon(ss.state) <> " "
    Error(_) -> "  "
  }

  // Priority badge
  let priority_badge =
    "[" <> task.priority_display(task.priority) <> "]"
  let priority_color = theme.priority_color(colors, task.priority_to_int(task.priority))

  // Type icon
  let type_icon = task.type_icon(task.issue_type)

  // Title (truncated)
  let available_width = width - 10
  // Account for prefix, state, priority
  let title = truncate(task.title, available_width)

  // Build card line
  let line1 =
    hbox([
      styled_text(cursor_prefix, sem.cursor),
      styled_text(state_indicator, session_color(session_state, colors)),
      styled_text(priority_badge, priority_color),
      text(" "),
      styled_text(type_icon, colors.subtext0),
      text(" "),
      styled_text(task.id, colors.subtext0),
    ])

  let line2 = hbox([text("  "), styled_text(title, colors.text)])

  // Card with optional background highlight for cursor
  case is_cursor {
    True ->
      // Shore doesn't support hex colors, use plain vbox
      vbox([line1, line2])
    False -> vbox([line1, line2])
  }
}

fn session_color(
  state: Result(session.SessionState, Nil),
  colors: theme.Colors,
) -> String {
  case state {
    Ok(ss) -> theme.session_color(colors, session.state_to_string(ss.state))
    Error(_) -> colors.subtext0
  }
}
