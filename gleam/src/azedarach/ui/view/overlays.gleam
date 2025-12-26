// Overlay views - action menu, filter, help, etc.

import gleam/dict
import gleam/int
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/set
import gleam/string
import shore/ui
import shore/style
import azedarach/config
import azedarach/domain/session
import azedarach/domain/task
import azedarach/ui/model.{type Model, type Overlay}
import azedarach/ui/theme
import azedarach/ui/view/utils.{
  type Node, bold_text, bordered_box, dim_text, hbox, styled_text, text, vbox,
}

pub fn render(overlay: Overlay, model: Model) -> Node {
  case overlay {
    model.ActionMenu -> render_action_menu(model)
    model.SortMenu -> render_sort_menu(model)
    model.FilterMenu -> render_filter_menu(model)
    model.StatusFilterMenu -> render_status_filter_menu(model)
    model.PriorityFilterMenu -> render_priority_filter_menu(model)
    model.TypeFilterMenu -> render_type_filter_menu(model)
    model.SessionFilterMenu -> render_session_filter_menu(model)
    model.HelpOverlay -> render_help(model)
    model.SettingsOverlay(focus_index) -> render_settings(focus_index, model)
    model.DiagnosticsOverlay -> render_diagnostics(model)
    model.LogsViewer -> render_logs(model)
    model.ProjectSelector -> render_project_selector(model)
    model.DetailPanel(bead_id) -> render_detail_panel(bead_id, model)
    model.ImageAttach(bead_id) -> render_image_attach(bead_id, model)
    model.ImagePreview(path) -> render_image_preview(path, model)
    model.DevServerMenu(bead_id) -> render_dev_server_menu(bead_id, model)
    model.DiffViewer(bead_id) -> render_diff_viewer(bead_id, model)
    model.MergeChoice(bead_id, behind) -> render_merge_choice(bead_id, behind, model)
    model.ConfirmDialog(action) -> render_confirm(action, model)
  }
}

fn render_action_menu(model: Model) -> Node {
  let colors = model.colors

  let items = [
    #("Session", [
      #("s", "Start session"),
      #("S", "Start+work"),
      #("!", "Start yolo"),
      #("a", "Attach"),
      #("p", "Pause"),
      #("R", "Resume"),
      #("x", "Stop"),
    ]),
    #("Dev Server", [
      #("r", "Toggle server"),
      #("v", "View server"),
      #("^r", "Restart"),
    ]),
    #("Git", [
      #("u", "Update from main"),
      #("m", "Merge to main"),
      #("f", "Show diff"),
      #("P", "Create PR"),
      #("d", "Delete/cleanup"),
    ]),
    #("Task", [
      #("h", "Move left"),
      #("l", "Move right"),
    ]),
  ]

  let sections =
    list.map(items, fn(section) {
      let #(title, entries) = section
      let entry_elements =
        list.map(entries, fn(entry) {
          let #(key, desc) = entry
          hbox([
            styled_text(" " <> key <> " ", colors.yellow),
            text(desc),
          ])
        })
      vbox([bold_text(title, colors.mauve), ..entry_elements])
    })

  overlay_box("Actions", sections, model)
}

fn render_sort_menu(model: Model) -> Node {
  let colors = model.colors

  let items = [
    #("s", "Session status", model.sort_by == model.SortBySession),
    #("p", "Priority", model.sort_by == model.SortByPriority),
    #("u", "Updated", model.sort_by == model.SortByUpdated),
  ]

  let entries =
    list.map(items, fn(item) {
      let #(key, desc, active) = item
      let indicator = case active {
        True -> "●"
        False -> "○"
      }
      hbox([
        styled_text(" " <> key <> " ", colors.yellow),
        styled_text(indicator <> " ", colors.green),
        text(desc),
      ])
    })

  overlay_box("Sort", entries, model)
}

fn render_filter_menu(model: Model) -> Node {
  let colors = model.colors

  let status_count = set.size(model.status_filter)
  let priority_count = set.size(model.priority_filter)
  let type_count = set.size(model.type_filter)
  let session_count = set.size(model.session_filter)

  let items = [
    #("s", "Status", status_count),
    #("p", "Priority", priority_count),
    #("t", "Type", type_count),
    #("S", "Session state", session_count),
  ]

  let entries =
    list.map(items, fn(item) {
      let #(key, desc, count) = item
      let count_text = case count {
        0 -> ""
        n -> " (" <> int.to_string(n) <> ")"
      }
      hbox([
        styled_text(" " <> key <> " ", colors.yellow),
        text(desc),
        styled_text(count_text, colors.green),
        styled_text(" →", colors.subtext0),
      ])
    })

  let extra = [
    hbox([
      styled_text(" e ", colors.yellow),
      text("Hide epic children"),
      styled_text(
        case model.hide_epic_children {
          True -> " ●"
          False -> ""
        },
        colors.green,
      ),
    ]),
    hbox([styled_text(" c ", colors.yellow), text("Clear all")]),
  ]

  overlay_box("Filter", list.append(entries, extra), model)
}

fn render_status_filter_menu(model: Model) -> Node {
  let colors = model.colors

  let items = [
    #("o", "Open", task.Open),
    #("i", "In Progress", task.InProgress),
    #("r", "Review", task.Review),
    #("d", "Done", task.Done),
    #("b", "Blocked", task.Blocked),
  ]

  let entries =
    list.map(items, fn(item) {
      let #(key, desc, status) = item
      let active = set.contains(model.status_filter, status)
      let indicator = case active {
        True -> "●"
        False -> "○"
      }
      hbox([
        styled_text(" " <> key <> " ", colors.yellow),
        styled_text(indicator <> " ", colors.green),
        text(desc),
      ])
    })

  let footer = [
    text(""),
    dim_text("Esc: back  q: close"),
  ]

  overlay_box("Filter › Status", list.append(entries, footer), model)
}

fn render_priority_filter_menu(model: Model) -> Node {
  let colors = model.colors

  let items = [
    #("1", "P1 (Critical)", task.P1),
    #("2", "P2 (High)", task.P2),
    #("3", "P3 (Medium)", task.P3),
    #("4", "P4 (Low)", task.P4),
  ]

  let entries =
    list.map(items, fn(item) {
      let #(key, desc, priority) = item
      let active = set.contains(model.priority_filter, priority)
      let indicator = case active {
        True -> "●"
        False -> "○"
      }
      hbox([
        styled_text(" " <> key <> " ", colors.yellow),
        styled_text(indicator <> " ", colors.green),
        text(desc),
      ])
    })

  let footer = [
    text(""),
    dim_text("Esc: back  q: close"),
  ]

  overlay_box("Filter › Priority", list.append(entries, footer), model)
}

fn render_type_filter_menu(model: Model) -> Node {
  let colors = model.colors

  let items = [
    #("t", "Task", task.TaskType),
    #("b", "Bug", task.Bug),
    #("e", "Epic", task.Epic),
    #("f", "Feature", task.Feature),
    #("c", "Chore", task.Chore),
  ]

  let entries =
    list.map(items, fn(item) {
      let #(key, desc, issue_type) = item
      let active = set.contains(model.type_filter, issue_type)
      let indicator = case active {
        True -> "●"
        False -> "○"
      }
      hbox([
        styled_text(" " <> key <> " ", colors.yellow),
        styled_text(indicator <> " ", colors.green),
        text(desc),
      ])
    })

  let footer = [
    text(""),
    dim_text("Esc: back  q: close"),
  ]

  overlay_box("Filter › Type", list.append(entries, footer), model)
}

fn render_session_filter_menu(model: Model) -> Node {
  let colors = model.colors

  let items = [
    #("i", "Idle", session.Idle),
    #("b", "Busy", session.Busy),
    #("w", "Waiting", session.Waiting),
    #("d", "Done", session.Done),
    #("e", "Error", session.Error),
    #("p", "Paused", session.Paused),
  ]

  let entries =
    list.map(items, fn(item) {
      let #(key, desc, state) = item
      let active = set.contains(model.session_filter, state)
      let indicator = case active {
        True -> "●"
        False -> "○"
      }
      hbox([
        styled_text(" " <> key <> " ", colors.yellow),
        styled_text(indicator <> " ", colors.green),
        text(desc),
      ])
    })

  let footer = [
    text(""),
    dim_text("Esc: back  q: close"),
  ]

  overlay_box("Filter › Session", list.append(entries, footer), model)
}

fn render_help(model: Model) -> Node {
  let colors = model.colors

  let sections = [
    #("Navigation", [
      "h/j/k/l or arrows  Move cursor",
      "Ctrl+Shift+d/u     Page down/up",
      "Enter              View details",
      "q                  Quit",
    ]),
    #("Modes", [
      "Space              Action menu",
      "/                  Search",
      "f                  Filter",
      ",                  Sort",
      "v                  Select mode",
      "g                  Goto mode",
    ]),
    #("Goto (g+)", [
      "g                  First in column",
      "e                  Last in column",
      "h/l                First/last column",
      "p                  Project selector",
      "w                  Jump labels",
    ]),
    #("Quick", [
      "c                  Create bead",
      "C                  Create with Claude",
      "s                  Settings",
      "d                  Diagnostics",
      "?                  This help",
      "R                  Refresh",
    ]),
  ]

  let section_elements =
    list.map(sections, fn(section) {
      let #(title, lines) = section
      vbox([
        bold_text(title, colors.mauve),
        ..list.map(lines, fn(line) { dim_text("  " <> line) })
      ])
    })

  overlay_box("Help", section_elements, model)
}

fn render_settings(focus_index: Int, model: Model) -> Node {
  let colors = model.colors
  let settings = config.editable_settings()

  let entries =
    list.index_map(settings, fn(setting, idx) {
      let is_focused = idx == focus_index
      let value = { setting.get_value }(model.config)
      let value_str = config.setting_value_to_string(value)

      let indicator = case is_focused {
        True -> ">"
        False -> " "
      }

      let label_color = case is_focused {
        True -> colors.yellow
        False -> colors.subtext0
      }
      let value_color = case is_focused {
        True -> colors.green
        False -> colors.text
      }

      hbox([
        styled_text(indicator <> " ", colors.yellow),
        styled_text(setting.label <> ": ", label_color),
        styled_text(value_str, value_color),
      ])
    })

  let footer = [
    text(""),
    dim_text("j/k: navigate • Space/Enter: toggle • Esc: close"),
  ]

  overlay_box("Settings", list.append(entries, footer), model)
}

fn render_diagnostics(model: Model) -> Node {
  let colors = model.colors

  let stats = [
    #("Tasks", int.to_string(list.length(model.tasks))),
    #("Active sessions", int.to_string(dict.size(model.sessions))),
    #("Dev servers", int.to_string(dict.size(model.dev_servers))),
    #("Terminal size", int.to_string(model.terminal_size.0) <> "x" <> int.to_string(model.terminal_size.1)),
  ]

  let entries =
    list.map(stats, fn(stat) {
      let #(label, value) = stat
      hbox([
        styled_text(label <> ": ", colors.subtext0),
        styled_text(value, colors.green),
      ])
    })

  overlay_box("Diagnostics", entries, model)
}

fn render_logs(model: Model) -> Node {
  // Placeholder - would show actual logs
  overlay_box("Logs", [dim_text("(no logs)")], model)
}

fn render_project_selector(model: Model) -> Node {
  let colors = model.colors

  let projects =
    list.index_map(model.projects, fn(project, idx) {
      let is_current = Some(project.name) == model.current_project
      let indicator = case is_current {
        True -> "●"
        False -> " "
      }
      hbox([
        styled_text(" " <> int.to_string(idx + 1) <> " ", colors.yellow),
        styled_text(indicator <> " ", colors.green),
        styled_text(project.name, colors.text),
        dim_text(" " <> project.path),
      ])
    })

  case list.is_empty(projects) {
    True -> overlay_box("Projects", [dim_text("(no projects configured)")], model)
    False -> overlay_box("Select Project", projects, model)
  }
}

fn render_detail_panel(bead_id: String, model: Model) -> Node {
  let colors = model.colors

  // Find task
  case list.find(model.tasks, fn(t) { t.id == bead_id }) {
    Ok(task) -> {
      let lines = [
        hbox([styled_text("ID: ", colors.subtext0), text(task.id)]),
        hbox([styled_text("Title: ", colors.subtext0), text(task.title)]),
        hbox([styled_text("Status: ", colors.subtext0), text(task.status_display(task.status))]),
        hbox([styled_text("Priority: ", colors.subtext0), text(task.priority_display(task.priority))]),
        hbox([styled_text("Type: ", colors.subtext0), text(task.issue_type_to_string(task.issue_type))]),
        text(""),
        bold_text("Description:", colors.mauve),
        dim_text(task.description),
      ]

      // Add design notes if present
      let with_notes = case task.design {
        Some(design) -> list.append(lines, [
          text(""),
          bold_text("Design Notes:", colors.mauve),
          dim_text(design),
        ])
        None -> lines
      }

      overlay_box(bead_id, with_notes, model)
    }
    Error(_) -> overlay_box(bead_id, [dim_text("(not found)")], model)
  }
}

fn render_image_attach(bead_id: String, model: Model) -> Node {
  let colors = model.colors

  let items = [
    hbox([styled_text(" p/v ", colors.yellow), text("Paste from clipboard")]),
    hbox([styled_text(" f ", colors.yellow), text("Enter file path")]),
    hbox([styled_text(" Esc ", colors.subtext0), text("Cancel")]),
  ]

  overlay_box("Attach Image to " <> bead_id, items, model)
}

fn render_image_preview(_path: String, model: Model) -> Node {
  overlay_box("Image Preview", [dim_text("(preview not available in terminal)")], model)
}

fn render_dev_server_menu(bead_id: String, model: Model) -> Node {
  // Show configured servers
  let servers = model.config.dev_server.servers
  let entries =
    list.map(servers, fn(s) {
      hbox([
        styled_text(s.name, model.colors.text),
        dim_text(" - " <> s.command),
      ])
    })

  overlay_box("Dev Servers for " <> bead_id, entries, model)
}

fn render_diff_viewer(_bead_id: String, model: Model) -> Node {
  overlay_box("Diff", [dim_text("(loading diff...)")], model)
}

fn render_merge_choice(bead_id: String, behind: Int, model: Model) -> Node {
  let colors = model.colors

  let items = [
    text(int.to_string(behind) <> " commits behind main"),
    text(""),
    text("Merge main into your branch before attaching?"),
    text(""),
    hbox([styled_text(" m ", colors.yellow), text("Merge & Attach")]),
    hbox([styled_text(" s ", colors.yellow), text("Skip & Attach")]),
    hbox([styled_text(" Esc ", colors.subtext0), text("Cancel")]),
  ]

  overlay_box("↓ Branch Behind main", items, model)
}

fn render_confirm(action: model.PendingAction, model: Model) -> Node {
  let colors = model.colors

  let #(title, description) = case action {
    model.DeleteWorktreeAction(id) -> #(
      "Cleanup " <> id <> "?",
      "This will: kill session, delete worktree, delete branch",
    )
    model.DeleteBeadAction(id) -> #(
      "Delete " <> id <> "?",
      "This will permanently delete the bead",
    )
    model.StopSessionAction(id) -> #(
      "Stop session " <> id <> "?",
      "This will kill the tmux session",
    )
  }

  let items = [
    text(description),
    text(""),
    hbox([styled_text(" y ", colors.yellow), text("Confirm")]),
    hbox([styled_text(" n/Esc ", colors.subtext0), text("Cancel")]),
  ]

  overlay_box(title, items, model)
}

// Helper to create overlay box
fn overlay_box(title: String, content: List(Node), model: Model) -> Node {
  let colors = model.colors
  let sem = theme.semantic(colors)

  bordered_box(
    [bold_text(title, colors.mauve), text(""), ..content],
    sem.border_focused,
  )
}
