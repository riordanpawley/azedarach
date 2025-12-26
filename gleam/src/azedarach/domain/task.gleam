// Task domain type - maps to beads issues

import gleam/option.{type Option}
import gleam/order.{type Order}

/// Full bead/task representation matching bd CLI JSON output
pub type Task {
  Task(
    id: String,
    title: String,
    description: String,
    status: Status,
    priority: Priority,
    issue_type: IssueType,
    parent_id: Option(String),
    created_at: String,
    updated_at: String,
    // Extended fields matching BeadsClient
    design: Option(String),
    notes: Option(String),
    acceptance: Option(String),
    assignee: Option(String),
    labels: List(String),
    estimate: Option(String),
    // Dependency tracking
    dependents: List(Dependent),
    blockers: List(String),
    // Legacy attachments field
    attachments: List(String),
    // Is this a tombstone (deleted)?
    is_tombstone: Bool,
  )
}

/// Dependent relationship for epics and blocking
pub type Dependent {
  Dependent(id: String, dep_type: DependentType)
}

pub type DependentType {
  ParentChild
  Blocks
  RelatedTo
}

pub type Status {
  Open
  InProgress
  Review
  Done
  Blocked
}

pub type Priority {
  P1
  P2
  P3
  P4
}

pub type IssueType {
  TaskType
  Bug
  Epic
  Feature
  Chore
}

// Status conversions

pub fn status_to_string(status: Status) -> String {
  case status {
    Open -> "open"
    InProgress -> "in_progress"
    Review -> "review"
    Done -> "done"
    Blocked -> "blocked"
  }
}

pub fn status_from_string(s: String) -> Status {
  case s {
    "open" | "backlog" -> Open
    "in_progress" -> InProgress
    "review" -> Review
    "done" | "closed" -> Done
    "blocked" -> Blocked
    _ -> Open
  }
}

pub fn status_display(status: Status) -> String {
  case status {
    Open -> "Open"
    InProgress -> "In Progress"
    Review -> "Review"
    Done -> "Done"
    Blocked -> "Blocked"
  }
}

// Priority conversions

pub fn priority_to_int(p: Priority) -> Int {
  case p {
    P1 -> 1
    P2 -> 2
    P3 -> 3
    P4 -> 4
  }
}

pub fn priority_from_int(n: Int) -> Priority {
  case n {
    1 -> P1
    2 -> P2
    3 -> P3
    _ -> P4
  }
}

pub fn priority_to_string(p: Priority) -> String {
  case p {
    P1 -> "P1"
    P2 -> "P2"
    P3 -> "P3"
    P4 -> "P4"
  }
}

pub fn priority_from_string(s: String) -> Priority {
  case s {
    "P1" | "p1" | "1" -> P1
    "P2" | "p2" | "2" -> P2
    "P3" | "p3" | "3" -> P3
    _ -> P4
  }
}

pub fn priority_display(p: Priority) -> String {
  priority_to_string(p)
}

pub fn compare_priority(a: Priority, b: Priority) -> Order {
  let a_int = priority_to_int(a)
  let b_int = priority_to_int(b)
  case a_int < b_int {
    True -> order.Lt
    False ->
      case a_int > b_int {
        True -> order.Gt
        False -> order.Eq
      }
  }
}

// Issue type conversions

pub fn issue_type_to_string(t: IssueType) -> String {
  case t {
    TaskType -> "task"
    Bug -> "bug"
    Epic -> "epic"
    Feature -> "feature"
    Chore -> "chore"
  }
}

pub fn issue_type_from_string(s: String) -> IssueType {
  case s {
    "task" -> TaskType
    "bug" -> Bug
    "epic" -> Epic
    "feature" -> Feature
    "chore" -> Chore
    _ -> TaskType
  }
}

pub fn type_icon(t: IssueType) -> String {
  case t {
    TaskType -> "T"
    Bug -> "B"
    Epic -> "E"
    Feature -> "F"
    Chore -> "C"
  }
}

pub fn type_display(t: IssueType) -> String {
  case t {
    TaskType -> "Task"
    Bug -> "Bug"
    Epic -> "Epic"
    Feature -> "Feature"
    Chore -> "Chore"
  }
}

// Dependent type conversions

pub fn dependent_type_to_string(t: DependentType) -> String {
  case t {
    ParentChild -> "parent-child"
    Blocks -> "blocks"
    RelatedTo -> "related-to"
  }
}

pub fn dependent_type_from_string(s: String) -> DependentType {
  case s {
    "parent-child" -> ParentChild
    "blocks" -> Blocks
    "related-to" -> RelatedTo
    _ -> RelatedTo
  }
}

// Task utilities

/// Check if task is an epic
pub fn is_epic(task: Task) -> Bool {
  task.issue_type == Epic
}

/// Check if task has a parent (is a child of an epic)
pub fn has_parent(task: Task) -> Bool {
  option.is_some(task.parent_id)
}

/// Get epic children from dependents
pub fn get_children(task: Task) -> List(String) {
  task.dependents
  |> list.filter_map(fn(dep) {
    case dep.dep_type {
      ParentChild -> Ok(dep.id)
      _ -> Error(Nil)
    }
  })
}

/// Check if task is blocked
pub fn is_blocked(task: Task) -> Bool {
  task.status == Blocked || list.length(task.blockers) > 0
}

/// Create an empty/default task for new bead creation
pub fn empty(id: String) -> Task {
  Task(
    id: id,
    title: "",
    description: "",
    status: Open,
    priority: P2,
    issue_type: TaskType,
    parent_id: option.None,
    created_at: "",
    updated_at: "",
    design: option.None,
    notes: option.None,
    acceptance: option.None,
    assignee: option.None,
    labels: [],
    estimate: option.None,
    dependents: [],
    blockers: [],
    attachments: [],
    is_tombstone: False,
  )
}

import gleam/list
