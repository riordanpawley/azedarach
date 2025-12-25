// Kanban board view - 4 columns

import gleam/dict
import gleam/int
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/string
import azedarach/domain/task.{type Task}
import azedarach/domain/session
import azedarach/ui/model.{type Model, Cursor}
import azedarach/ui/theme
import azedarach/ui/view.{
  type Element, Box, BoxProps, Column, Empty, Row, Text, TextProps,
  bordered_box, dim_text, hbox, pad_right, styled_text, text, truncate, vbox,
}

const column_names = ["Backlog", "In Progress", "Review", "Done"]

pub fn render(model: Model) -> Element {
  let #(width, height) = model.terminal_size
  let column_width = width / 4
  let board_height = height - 1
  // Reserve 1 line for status bar

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
  height: Int,
) -> Element {
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
    True -> [dim_text(view.center("(empty)", width - 2))]
    False -> cards
  }

  Box(
    [header, ..content],
    BoxProps(
      direction: Column,
      width: Some(width),
      height: Some(height),
      padding: 0,
      border: True,
      bg: Some(colors.base),
      fg: Some(
        case is_selected {
          True -> sem.border_focused
          False -> sem.border
        },
      ),
    ),
  )
}

fn render_header(
  name: String,
  width: Int,
  color: String,
  is_selected: Bool,
  sem: theme.SemanticColors,
) -> Element {
  let indicator = case is_selected {
    True -> "▶ "
    False -> "  "
  }
  let header_text = indicator <> name
  let padded = pad_right(header_text, width - 2)

  Text(
    padded,
    TextProps(fg: Some(color), bg: None, bold: is_selected, dim: False),
  )
}

fn render_card(
  task: Task,
  session_state: Result(session.SessionState, Nil),
  width: Int,
  is_cursor: Bool,
  model: Model,
) -> Element {
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

  // Card background
  let bg = case is_cursor {
    True -> Some(colors.surface0)
    False -> None
  }

  Box(
    [line1, line2],
    BoxProps(
      direction: Column,
      width: Some(width),
      height: None,
      padding: 0,
      border: False,
      bg: bg,
      fg: None,
    ),
  )
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
