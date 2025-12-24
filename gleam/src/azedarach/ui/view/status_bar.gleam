// Status bar - bottom line with mode, project, session info

import gleam/dict
import gleam/int
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/string
import azedarach/domain/session
import azedarach/ui/model.{type Model, type Mode, Normal, Select}
import azedarach/ui/theme
import azedarach/ui/view.{
  type Element, Box, BoxProps, Row, Text, TextProps, hbox, pad_left, pad_right,
  styled_text, text,
}

pub fn render(model: Model) -> Element {
  let #(width, _height) = model.terminal_size
  let colors = model.colors
  let sem = theme.semantic(colors)

  // Left side: mode + project
  let left = render_left(model)

  // Center: search query if active
  let center = render_center(model)

  // Right side: session counts, dev server port
  let right = render_right(model)

  // Calculate spacing
  let left_len = element_length(left)
  let center_len = element_length(center)
  let right_len = element_length(right)
  let spacer_len = width - left_len - center_len - right_len

  let spacer1_len = spacer_len / 2
  let spacer2_len = spacer_len - spacer1_len

  Box(
    [
      left,
      text(string.repeat(" ", spacer1_len)),
      center,
      text(string.repeat(" ", spacer2_len)),
      right,
    ],
    BoxProps(
      direction: Row,
      width: Some(width),
      height: Some(1),
      padding: 0,
      border: False,
      bg: Some(colors.surface0),
      fg: Some(colors.text),
    ),
  )
}

fn render_left(model: Model) -> Element {
  let colors = model.colors
  let sem = theme.semantic(colors)

  // Mode indicator
  let mode_text = case model.mode {
    Normal -> " NORMAL "
    Select(selected) -> " SELECT(" <> int.to_string(set.size(selected)) <> ") "
  }

  let mode_bg = case model.mode {
    Normal -> colors.blue
    Select(_) -> colors.mauve
  }

  // Project name
  let project = case model.current_project {
    Some(p) -> " " <> p <> " "
    None -> ""
  }

  // Pending key indicator (for goto mode)
  let pending = case model.pending_key {
    Some(k) -> " g+" <> k
    None -> ""
  }

  hbox([
    Text(mode_text, TextProps(fg: Some(colors.base), bg: Some(mode_bg), bold: True, dim: False)),
    styled_text(project, colors.text),
    styled_text(pending, colors.yellow),
  ])
}

fn render_center(model: Model) -> Element {
  let colors = model.colors

  case model.input {
    Some(model.SearchInput(query)) ->
      hbox([
        styled_text("/", colors.yellow),
        styled_text(query, colors.text),
        styled_text("█", colors.yellow),
      ])
    _ -> view.Empty
  }
}

fn render_right(model: Model) -> Element {
  let colors = model.colors
  let sem = theme.semantic(colors)

  // Count sessions by state
  let busy_count =
    dict.values(model.sessions)
    |> list.filter(fn(s) { s.state == session.Busy })
    |> list.length

  let waiting_count =
    dict.values(model.sessions)
    |> list.filter(fn(s) { s.state == session.Waiting })
    |> list.length

  // Dev server ports
  let dev_ports =
    dict.values(model.dev_servers)
    |> list.filter(fn(s) { s.running })
    |> list.filter_map(fn(s) { option.to_result(s.port, Nil) })
    |> list.map(int.to_string)
    |> string.join(",")

  let dev_text = case dev_ports {
    "" -> ""
    ports -> " DEV:" <> ports <> " "
  }

  // Session status
  let session_text = case busy_count, waiting_count {
    0, 0 -> ""
    b, 0 -> " " <> int.to_string(b) <> "● "
    0, w -> " " <> int.to_string(w) <> "○ "
    b, w -> " " <> int.to_string(b) <> "● " <> int.to_string(w) <> "○ "
  }

  // Loading indicator
  let loading_text = case model.loading {
    True -> " ⟳ "
    False -> ""
  }

  // Help hint
  let help_hint = " ?:help "

  hbox([
    styled_text(loading_text, colors.blue),
    styled_text(dev_text, colors.green),
    styled_text(session_text, colors.blue),
    styled_text(help_hint, colors.subtext0),
  ])
}

// Approximate element length for spacing calculation
fn element_length(el: Element) -> Int {
  case el {
    Text(content, _) -> string.length(content)
    Box(children, _) ->
      list.fold(children, 0, fn(acc, child) { acc + element_length(child) })
    view.Empty -> 0
  }
}

import gleam/set
