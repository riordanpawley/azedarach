// Task domain type - maps to beads issues

import gleam/option.{type Option}
import gleam/order.{type Order}

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
    design_notes: Option(String),
    actor: Option(String),
    attachments: List(String),
  )
}

pub type Status {
  Backlog
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
  Task
  Bug
  Epic
  Feature
  Chore
}

pub fn status_to_string(status: Status) -> String {
  case status {
    Backlog -> "backlog"
    InProgress -> "in_progress"
    Review -> "review"
    Done -> "done"
    Blocked -> "blocked"
  }
}

pub fn status_from_string(s: String) -> Status {
  case s {
    "backlog" | "open" -> Backlog
    "in_progress" -> InProgress
    "review" -> Review
    "done" | "closed" -> Done
    "blocked" -> Blocked
    _ -> Backlog
  }
}

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

pub fn issue_type_to_string(t: IssueType) -> String {
  case t {
    Task -> "task"
    Bug -> "bug"
    Epic -> "epic"
    Feature -> "feature"
    Chore -> "chore"
  }
}

pub fn issue_type_from_string(s: String) -> IssueType {
  case s {
    "task" -> Task
    "bug" -> Bug
    "epic" -> Epic
    "feature" -> Feature
    "chore" -> Chore
    _ -> Task
  }
}

pub fn status_display(status: Status) -> String {
  case status {
    Backlog -> "Backlog"
    InProgress -> "In Progress"
    Review -> "Review"
    Done -> "Done"
    Blocked -> "Blocked"
  }
}

pub fn priority_display(p: Priority) -> String {
  case p {
    P1 -> "P1"
    P2 -> "P2"
    P3 -> "P3"
    P4 -> "P4"
  }
}

pub fn type_icon(t: IssueType) -> String {
  case t {
    Task -> "T"
    Bug -> "B"
    Epic -> "E"
    Feature -> "F"
    Chore -> "C"
  }
}
