// TEA Model - All application state

import gleam/dict.{type Dict}
import gleam/erlang/process.{type Subject}
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/order
import gleam/set.{type Set}
import gleam/string
import optimist.{type Optimistic}
import azedarach/config.{type Config}
import azedarach/domain/task.{type Task}
import azedarach/domain/session.{type SessionState}
import azedarach/domain/project.{type Project}
import azedarach/ui/theme.{type Colors}
import azedarach/actors/app_supervisor.{type AppContext}
import azedarach/actors/coordinator

// Main model holding all application state
pub type Model {
  Model(
    // Core data
    tasks: List(Task),
    sessions: Dict(String, SessionState),
    dev_servers: Dict(String, DevServerState),
    // Optimistic updates - tracks pending status changes
    // Key: task id, Value: optimistic status wrapper
    optimistic_statuses: Dict(String, Optimistic(task.Status)),
    // Multi-project
    projects: List(Project),
    current_project: Option(String),
    // Navigation
    cursor: Cursor,
    mode: Mode,
    // UI state
    input: Option(InputState),
    overlay: Option(Overlay),
    pending_key: Option(String),
    // Filters
    status_filter: Set(task.Status),
    priority_filter: Set(task.Priority),
    type_filter: Set(task.IssueType),
    session_filter: Set(session.State),
    hide_epic_children: Bool,
    sort_by: SortField,
    search_query: String,
    // Config and theme
    config: Config,
    colors: Colors,
    // Meta
    loading: Bool,
    toasts: List(Toast),
    terminal_size: #(Int, Int),
    // OTP supervision context (optional for backwards compatibility)
    app_context: Option(AppContext),
    // Subscription to receive coordinator messages (for optimistic updates)
    ui_subscription: Option(Subject(coordinator.UiMsg)),
  )
}

pub type Cursor {
  Cursor(column_index: Int, task_index: Int)
}

pub type Mode {
  Normal
  Select(selected: Set(String))
}

pub type InputState {
  SearchInput(query: String)
  TitleInput(text: String)
  NotesInput(text: String)
  PathInput(text: String)
}

pub type Overlay {
  ActionMenu
  SortMenu
  FilterMenu
  HelpOverlay
  SettingsOverlay
  DiagnosticsOverlay
  LogsViewer
  ProjectSelector
  DetailPanel(bead_id: String)
  ImageAttach(bead_id: String)
  ImagePreview(path: String)
  DevServerMenu(bead_id: String)
  DiffViewer(bead_id: String)
  MergeChoice(bead_id: String, behind_count: Int)
  ConfirmDialog(action: PendingAction)
}

pub type PendingAction {
  DeleteWorktree(bead_id: String)
  DeleteBead(bead_id: String)
  StopSession(bead_id: String)
}

pub type SortField {
  SortBySession
  SortByPriority
  SortByUpdated
}

pub type Toast {
  Toast(message: String, level: ToastLevel, expires_at: Int)
}

pub type ToastLevel {
  Info
  Success
  Warning
  Error
}

pub type DevServerState {
  DevServerState(
    name: String,
    running: Bool,
    port: Option(Int),
    window_name: String,
  )
}

// Messages
pub type Msg {
  // Navigation
  MoveUp
  MoveDown
  MoveLeft
  MoveRight
  PageUp
  PageDown
  GotoFirst
  GotoLast
  GotoColumn(Int)
  // Mode changes
  EnterSelect
  ExitSelect
  ToggleSelection
  EnterGoto
  ExitGoto
  EnterSearch
  ExitSearch
  // Overlay
  OpenActionMenu
  OpenFilterMenu
  OpenSortMenu
  OpenHelp
  OpenSettings
  OpenDiagnostics
  OpenLogs
  OpenProjectSelector
  OpenDetailPanel
  CloseOverlay
  // Actions
  StartSession
  StartSessionWithWork
  StartSessionYolo
  AttachSession
  PauseSession
  ResumeSession
  StopSession
  ToggleDevServer
  ViewDevServer
  RestartDevServer
  UpdateFromMain
  MergeToMain
  ShowDiff
  CreatePR
  DeleteCleanup
  MoveTaskLeft
  MoveTaskRight
  CreateBead
  CreateBeadWithClaude
  EditBead
  DeleteBead
  // Image
  AttachImage
  PasteFromClipboard
  SelectFile
  PreviewImage(String)
  DeleteImage(String)
  // Input
  InputChar(String)
  InputBackspace
  InputSubmit
  InputCancel
  // Filter/Sort
  ToggleStatusFilter(task.Status)
  TogglePriorityFilter(task.Priority)
  ToggleTypeFilter(task.IssueType)
  ToggleSessionFilter(session.State)
  ToggleHideEpicChildren
  ClearFilters
  SetSort(SortField)
  // MergeChoice
  MergeAndAttach
  SkipAndAttach
  // Confirm
  ConfirmAction
  CancelAction
  // Data updates
  BeadsLoaded(List(Task))
  SessionStateChanged(String, SessionState)
  DevServerStateChanged(String, DevServerState)
  ToastExpired(Int)
  // Optimistic update responses
  TaskMoveSucceeded(id: String, new_status: task.Status)
  TaskMoveFailed(id: String, error: String)
  // System
  TerminalResized(Int, Int)
  Tick
  Quit
  ForceRedraw
  // Keyboard raw
  KeyPressed(key: String, modifiers: List(Modifier))
}

pub type Modifier {
  Ctrl
  Shift
  Alt
}

// Initialize model with config and theme (legacy, no supervision)
pub fn init(config: Config, colors: Colors) -> Model {
  Model(
    tasks: [],
    sessions: dict.new(),
    dev_servers: dict.new(),
    optimistic_statuses: dict.new(),
    projects: [],
    current_project: None,
    cursor: Cursor(column_index: 0, task_index: 0),
    mode: Normal,
    input: None,
    overlay: None,
    pending_key: None,
    status_filter: set.new(),
    priority_filter: set.new(),
    type_filter: set.new(),
    session_filter: set.new(),
    hide_epic_children: False,
    sort_by: SortBySession,
    search_query: "",
    config: config,
    colors: colors,
    loading: True,
    toasts: [],
    terminal_size: #(80, 24),
    app_context: None,
    ui_subscription: None,
  )
}

// Initialize model with config, theme, and subscription subject
pub fn init_with_subscription(
  config: Config,
  colors: Colors,
  subscription: Subject(coordinator.UiMsg),
) -> Model {
  Model(
    tasks: [],
    sessions: dict.new(),
    dev_servers: dict.new(),
    optimistic_statuses: dict.new(),
    projects: [],
    current_project: None,
    cursor: Cursor(column_index: 0, task_index: 0),
    mode: Normal,
    input: None,
    overlay: None,
    pending_key: None,
    status_filter: set.new(),
    priority_filter: set.new(),
    type_filter: set.new(),
    session_filter: set.new(),
    hide_epic_children: False,
    sort_by: SortBySession,
    search_query: "",
    config: config,
    colors: colors,
    loading: True,
    toasts: [],
    terminal_size: #(80, 24),
    app_context: None,
    ui_subscription: Some(subscription),
  )
}

// Initialize model with supervision context (preferred)
// The subscription subject should be set separately via init_with_context_and_subscription
pub fn init_with_context(
  config: Config,
  colors: Colors,
  context: AppContext,
) -> Model {
  Model(
    tasks: [],
    sessions: dict.new(),
    dev_servers: dict.new(),
    optimistic_statuses: dict.new(),
    projects: [],
    current_project: None,
    cursor: Cursor(column_index: 0, task_index: 0),
    mode: Normal,
    input: None,
    overlay: None,
    pending_key: None,
    status_filter: set.new(),
    priority_filter: set.new(),
    type_filter: set.new(),
    session_filter: set.new(),
    hide_epic_children: False,
    sort_by: SortBySession,
    search_query: "",
    config: config,
    colors: colors,
    loading: True,
    toasts: [],
    terminal_size: #(80, 24),
    app_context: Some(context),
    ui_subscription: None,
  )
}

// Initialize model with supervision context and subscription (best)
pub fn init_with_context_and_subscription(
  config: Config,
  colors: Colors,
  context: AppContext,
  subscription: Subject(coordinator.UiMsg),
) -> Model {
  Model(
    tasks: [],
    sessions: dict.new(),
    dev_servers: dict.new(),
    optimistic_statuses: dict.new(),
    projects: [],
    current_project: None,
    cursor: Cursor(column_index: 0, task_index: 0),
    mode: Normal,
    input: None,
    overlay: None,
    pending_key: None,
    status_filter: set.new(),
    priority_filter: set.new(),
    type_filter: set.new(),
    session_filter: set.new(),
    hide_epic_children: False,
    sort_by: SortBySession,
    search_query: "",
    config: config,
    colors: colors,
    loading: True,
    toasts: [],
    terminal_size: #(80, 24),
    app_context: Some(context),
    ui_subscription: Some(subscription),
  )
}

// Get tasks for a specific column
// Uses optimistic status when available for immediate UI feedback
pub fn tasks_in_column(model: Model, column: Int) -> List(Task) {
  let status = column_to_status(column)
  model.tasks
  |> apply_filters(model)
  |> apply_search(model.search_query)
  |> apply_sort(model.sort_by, model.sessions)
  |> list.filter(fn(t) { get_effective_status(t, model.optimistic_statuses) == status })
}

/// Get the effective status of a task, checking optimistic updates first
pub fn get_effective_status(
  t: Task,
  optimistic_statuses: Dict(String, Optimistic(task.Status)),
) -> task.Status {
  case dict.get(optimistic_statuses, t.id) {
    Ok(opt) -> optimist.unwrap(opt)
    Error(_) -> t.status
  }
}

/// Check if a task has a pending optimistic update
pub fn has_pending_update(model: Model, task_id: String) -> Bool {
  dict.has_key(model.optimistic_statuses, task_id)
}

fn column_to_status(column: Int) -> task.Status {
  case column {
    0 -> task.Open
    1 -> task.InProgress
    2 -> task.Review
    _ -> task.Done
  }
}

fn apply_filters(tasks: List(Task), model: Model) -> List(Task) {
  tasks
  |> filter_by_status(model.status_filter)
  |> filter_by_priority(model.priority_filter)
  |> filter_by_type(model.type_filter)
  |> filter_epic_children(model.hide_epic_children)
}

fn filter_by_status(tasks: List(Task), filter: Set(task.Status)) -> List(Task) {
  case set.is_empty(filter) {
    True -> tasks
    False -> list.filter(tasks, fn(t) { set.contains(filter, t.status) })
  }
}

fn filter_by_priority(
  tasks: List(Task),
  filter: Set(task.Priority),
) -> List(Task) {
  case set.is_empty(filter) {
    True -> tasks
    False -> list.filter(tasks, fn(t) { set.contains(filter, t.priority) })
  }
}

fn filter_by_type(tasks: List(Task), filter: Set(task.IssueType)) -> List(Task) {
  case set.is_empty(filter) {
    True -> tasks
    False -> list.filter(tasks, fn(t) { set.contains(filter, t.issue_type) })
  }
}

fn filter_epic_children(tasks: List(Task), hide: Bool) -> List(Task) {
  case hide {
    False -> tasks
    True -> list.filter(tasks, fn(t) { option.is_none(t.parent_id) })
  }
}

fn apply_search(tasks: List(Task), query: String) -> List(Task) {
  case query {
    "" -> tasks
    q -> {
      let lower_q = string.lowercase(q)
      list.filter(tasks, fn(t) {
        string.contains(string.lowercase(t.title), lower_q)
        || string.contains(string.lowercase(t.id), lower_q)
      })
    }
  }
}

fn apply_sort(
  tasks: List(Task),
  sort_by: SortField,
  sessions: Dict(String, SessionState),
) -> List(Task) {
  case sort_by {
    SortBySession ->
      list.sort(tasks, fn(a, b) {
        let a_state = dict.get(sessions, a.id) |> option.from_result
        let b_state = dict.get(sessions, b.id) |> option.from_result
        compare_session_state(a_state, b_state)
      })
    SortByPriority ->
      list.sort(tasks, fn(a, b) { task.compare_priority(a.priority, b.priority) })
    SortByUpdated ->
      list.sort(tasks, fn(a, b) { string.compare(b.updated_at, a.updated_at) })
  }
}

fn compare_session_state(
  a: Option(SessionState),
  b: Option(SessionState),
) -> order.Order {
  case a, b {
    Some(sa), Some(sb) -> session.compare_state(sa.state, sb.state)
    Some(_), None -> order.Lt
    None, Some(_) -> order.Gt
    None, None -> order.Eq
  }
}
