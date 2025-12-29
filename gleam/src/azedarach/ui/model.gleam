// TEA Model - All application state

import gleam/dict.{type Dict}
import gleam/erlang/process.{type Subject}
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/order
import gleam/set.{type Set}
import gleam/string
import azedarach/config.{type Config}
import azedarach/domain/task.{type Task}
import azedarach/domain/session
import azedarach/domain/project.{type Project}
import azedarach/ui/theme.{type Colors}
import azedarach/ui/textfield.{type TextField}
import azedarach/actors/app_supervisor.{type AppContext}

// Main model holding all application state
pub type Model {
  Model(
    // Core data
    tasks: List(Task),
    sessions: Dict(String, session.SessionState),
    dev_servers: Dict(String, DevServerState),
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
    current_epic: Option(String),
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
    // Exit signal - send to this subject to quit the app
    exit_subject: Option(Subject(Nil)),
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
  PathInput(path: String, bead_id: String)
}

pub type Overlay {
  ActionMenu
  SortMenu
  FilterMenu
  // Filter sub-menus
  StatusFilterMenu
  PriorityFilterMenu
  TypeFilterMenu
  SessionFilterMenu
  HelpOverlay
  SettingsOverlay(focus_index: Int)
  DiagnosticsOverlay
  LogsViewer
  ProjectSelector
  DetailPanel(bead_id: String)
  ImageAttach(bead_id: String)
  ImageList(bead_id: String)
  ImagePreview(path: String)
  DevServerMenu(bead_id: String)
  DiffViewer(bead_id: String)
  MergeChoice(bead_id: String, behind_count: Int, merge_in_progress: Bool)
  ConfirmDialog(action: PendingAction)
  // Planning workflow
  PlanningOverlay(state: PlanningOverlayState)
}

/// State for the planning overlay
pub type PlanningOverlayState {
  PlanningInput(field: TextField)
  PlanningGenerating(description: String)
  PlanningReviewing(description: String, pass: Int, max_passes: Int)
  PlanningCreatingBeads(description: String)
  PlanningComplete(created_ids: List(String))
  PlanningError(message: String)
}

pub type PendingAction {
  DeleteWorktreeAction(bead_id: String)
  DeleteBeadAction(bead_id: String)
  StopSessionAction(bead_id: String)
  DeleteImageAction(bead_id: String, attachment_id: String)
}

pub type SortField {
  SortBySession
  SortByPriority
  SortByUpdated
}

pub type Toast {
  Toast(id: Int, message: String, level: ToastLevel, expires_at: Int)
}

pub type ToastLevel {
  Info
  Success
  Warning
  ErrorLevel
}

/// Default toast duration in milliseconds
pub const toast_duration_ms = 5000

/// Longer duration for error toasts (to read suggestions)
pub const error_toast_duration_ms = 8000

/// Maximum visible toasts
pub const max_visible_toasts = 3

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
  OpenStatusFilterMenu
  OpenPriorityFilterMenu
  OpenTypeFilterMenu
  OpenSessionFilterMenu
  OpenSortMenu
  OpenHelp
  OpenSettings
  OpenDiagnostics
  OpenLogs
  OpenProjectSelector
  OpenDetailPanel
  CloseOverlay
  // Epic drill-down
  DrillDownEpic(bead_id: String)
  ExitEpicDrill
  // Project selection
  SelectProject(index: Int)
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
  CompleteSession
  DeleteCleanup
  MoveTaskLeft
  MoveTaskRight
  CreateBead
  CreateBeadWithClaude
  EditBead
  DeleteBead
  // Image
  AttachImage
  OpenImageList
  PasteFromClipboard
  SelectFile
  AttachFileSubmit(bead_id: String, path: String)
  OpenImage(bead_id: String, attachment_id: String)
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
  AbortMerge
  // Confirm
  ConfirmAction
  CancelAction
  // Settings
  SettingsNavigateUp
  SettingsNavigateDown
  SettingsToggleCurrent
  SettingsSaved(Result(Nil, config.ConfigError))
  // Data updates
  BeadsLoaded(List(Task))
  SessionStateChanged(String, session.SessionState)
  DevServerStateChanged(String, DevServerState)
  // Toast notifications
  ShowToast(level: ToastLevel, message: String)
  ToastExpired(Int)
  // Coordinator events
  RequestMergeChoice(bead_id: String, behind_count: Int, merge_in_progress: Bool)
  ProjectChanged(project: Project)
  ProjectsUpdated(projects: List(Project))
  // System
  TerminalResized(Int, Int)
  Tick
  Quit
  ForceRedraw
  // Keyboard raw
  KeyPressed(key: String, modifiers: List(Modifier))
  // Planning workflow
  OpenPlanning
  PlanningFieldUpdate(TextField)
  PlanningSubmit
  PlanningCancel
  PlanningStateUpdated(PlanningOverlayState)
  PlanningAttachSession
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
    current_epic: None,
    sort_by: SortBySession,
    search_query: "",
    config: config,
    colors: colors,
    loading: True,
    toasts: [],
    terminal_size: #(80, 24),
    app_context: None,
    exit_subject: None,
  )
}

// Initialize model with supervision context (preferred)
pub fn init_with_context(
  config: Config,
  colors: Colors,
  context: AppContext,
  exit_subj: Subject(Nil),
) -> Model {
  Model(
    tasks: [],
    sessions: dict.new(),
    dev_servers: dict.new(),
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
    current_epic: None,
    sort_by: SortBySession,
    search_query: "",
    config: config,
    colors: colors,
    loading: True,
    toasts: [],
    terminal_size: #(80, 24),
    app_context: Some(context),
    exit_subject: Some(exit_subj),
  )
}

// =============================================================================
// Toast helpers
// =============================================================================

/// Add a toast to the model with proper expiration
/// Returns the updated model and the toast ID for scheduling expiration
pub fn add_toast(model: Model, level: ToastLevel, message: String, now_ms: Int) -> #(Model, Int) {
  let duration = case level {
    ErrorLevel -> error_toast_duration_ms
    _ -> toast_duration_ms
  }
  let expires_at = now_ms + duration
  let toast = Toast(id: expires_at, message: message, level: level, expires_at: expires_at)

  // Keep only the last (max_visible - 1) toasts and add the new one
  let kept_toasts = model.toasts
    |> list.reverse
    |> list.take(max_visible_toasts - 1)
    |> list.reverse

  let new_model = Model(..model, toasts: list.append(kept_toasts, [toast]))
  #(new_model, expires_at)
}

/// Get level icon for display
pub fn toast_icon(level: ToastLevel) -> String {
  case level {
    Info -> "i"
    Success -> "+"
    Warning -> "!"
    ErrorLevel -> "!"
  }
}

// Get tasks for a specific column
pub fn tasks_in_column(model: Model, column: Int) -> List(Task) {
  let status = column_to_status(column)
  model.tasks
  |> apply_filters(model)
  |> apply_search(model.search_query)
  |> apply_sort(model.sort_by, model.sessions)
  |> list.filter(fn(t) { t.status == status })
}

fn column_to_status(column: Int) -> task.Status {
  case column {
    0 -> task.Open
    1 -> task.InProgress
    2 -> task.Blocked
    _ -> task.Done
  }
}

fn apply_filters(tasks: List(Task), model: Model) -> List(Task) {
  tasks
  |> filter_by_current_epic(model.current_epic)
  |> filter_by_status(model.status_filter)
  |> filter_by_priority(model.priority_filter)
  |> filter_by_type(model.type_filter)
  |> filter_by_session_state(model.session_filter, model.sessions)
  |> filter_epic_children(model.hide_epic_children)
}

fn filter_by_current_epic(
  tasks: List(Task),
  epic_id: Option(String),
) -> List(Task) {
  case epic_id {
    None -> tasks
    Some(id) ->
      list.filter(tasks, fn(t) {
        // Include the epic itself
        t.id == id
        // Include children of this epic
        || option.map(t.parent_id, fn(pid) { pid == id })
        |> option.unwrap(False)
      })
  }
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

fn filter_by_session_state(
  tasks: List(Task),
  filter: Set(session.State),
  sessions: Dict(String, session.SessionState),
) -> List(Task) {
  case set.is_empty(filter) {
    True -> tasks
    False ->
      list.filter(tasks, fn(t) {
        case dict.get(sessions, t.id) {
          Ok(session_state) -> set.contains(filter, session_state.state)
          // If no session, treat as Idle
          Error(_) -> set.contains(filter, session.Idle)
        }
      })
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
  sessions: Dict(String, session.SessionState),
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
  a: Option(session.SessionState),
  b: Option(session.SessionState),
) -> order.Order {
  case a, b {
    Some(sa), Some(sb) -> session.compare_state(sa.state, sb.state)
    Some(_), None -> order.Lt
    None, Some(_) -> order.Gt
    None, None -> order.Eq
  }
}

// =============================================================================
// Epic drill-down helpers
// =============================================================================

/// Check if we're currently in epic drill-down mode
pub fn in_epic_drill(model: Model) -> Bool {
  option.is_some(model.current_epic)
}

/// Get the current epic task if in drill-down mode
pub fn get_current_epic(model: Model) -> Option(Task) {
  case model.current_epic {
    None -> None
    Some(id) ->
      list.find(model.tasks, fn(t) { t.id == id })
      |> option.from_result
  }
}

/// Get epic children (not including the epic itself)
pub fn get_epic_children(model: Model, epic_id: String) -> List(Task) {
  list.filter(model.tasks, fn(t) {
    case t.parent_id {
      Some(pid) -> pid == epic_id
      None -> False
    }
  })
}

/// Calculate epic progress as (completed, total)
pub fn epic_progress(model: Model, epic_id: String) -> #(Int, Int) {
  let children = get_epic_children(model, epic_id)
  let total = list.length(children)
  let completed =
    list.count(children, fn(t) { t.status == task.Done })
  #(completed, total)
}
