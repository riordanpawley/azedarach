// Planning Service - AI-powered task planning with Claude Code
//
// Uses a tmux session running Claude Code to:
// 1. Generate implementation plans from feature descriptions
// 2. Iteratively review and refine plans (4-5 passes)
// 3. Generate beads with proper epic/task hierarchy and dependencies
//
// The plan is optimized for parallelized development with small tasks.

import gleam/dynamic/decode
import gleam/int
import gleam/json
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/result
import gleam/string
import azedarach/config.{type Config}
import azedarach/domain/task
import azedarach/services/beads
import azedarach/services/tmux
import azedarach/util/shell

// ============================================================================
// Types
// ============================================================================

pub type PlanningError {
  TmuxError(tmux.TmuxError)
  ParseError(message: String)
  BeadsError(beads.BeadsError)
  SessionNotReady(message: String)
  ReviewFailed(message: String)
}

pub fn error_to_string(err: PlanningError) -> String {
  case err {
    TmuxError(e) -> "Tmux error: " <> tmux.error_to_string(e)
    ParseError(msg) -> "Parse error: " <> msg
    BeadsError(e) -> "Beads error: " <> beads.error_to_string(e)
    SessionNotReady(msg) -> "Session not ready: " <> msg
    ReviewFailed(msg) -> "Review failed: " <> msg
  }
}

/// A planned task within the generated plan
pub type PlannedTask {
  PlannedTask(
    temp_id: String,        // Temporary ID for dependency linking (e.g., "task-1")
    title: String,
    description: String,
    issue_type: task.IssueType,
    priority: task.Priority,
    depends_on: List(String),  // Temp IDs of tasks this depends on
    can_parallelize: Bool,     // Can run in parallel with siblings
    design: Option(String),    // Technical design notes
    acceptance: Option(String), // Acceptance criteria
  )
}

/// The complete generated plan
pub type Plan {
  Plan(
    epic_title: String,
    epic_description: String,
    summary: String,
    tasks: List(PlannedTask),
    parallelization_score: Int,  // 0-100
  )
}

/// Planning session state
pub type PlanningState {
  Idle
  WaitingForInput
  Generating
  Reviewing(pass: Int, max_passes: Int)
  Refining(pass: Int)
  CreatingBeads
  Complete(created_ids: List(String))
  Error(message: String)
}

/// Planning session name
pub const planning_session = "az-planning"

/// Window name for Claude
pub const planning_window = "claude"

// ============================================================================
// Session Management
// ============================================================================

/// Start a planning session with Claude Code
///
/// Creates a tmux session running Claude Code for planning tasks.
/// The session persists between planning operations.
pub fn start_session(
  project_path: String,
  config: Config,
) -> Result(Nil, PlanningError) {
  // Check if session already exists
  case tmux.session_exists(planning_session) {
    True -> Ok(Nil)
    False -> {
      // Create new session
      use _ <- result.try(
        tmux.new_session(planning_session, project_path)
        |> result.map_error(TmuxError),
      )

      // Start Claude Code in the session (non-interactive mode for planning)
      use _ <- result.try(
        tmux.send_keys(
          planning_session <> ":" <> planning_window,
          "claude --model sonnet Enter",
        )
        |> result.map_error(TmuxError),
      )

      Ok(Nil)
    }
  }
}

/// Stop the planning session
pub fn stop_session() -> Result(Nil, PlanningError) {
  tmux.kill_session(planning_session)
  |> result.map_error(TmuxError)
}

/// Check if planning session exists
pub fn session_exists() -> Bool {
  tmux.session_exists(planning_session)
}

/// Attach to the planning session
pub fn attach_session() -> Result(Nil, PlanningError) {
  tmux.attach(planning_session)
  |> result.map_error(TmuxError)
}

// ============================================================================
// Planning Workflow
// ============================================================================

/// Generate a plan from a feature description
///
/// Sends the planning prompt to Claude Code and waits for the response.
/// Returns the generated plan as structured data.
pub fn generate_plan(
  feature_description: String,
  project_path: String,
  config: Config,
) -> Result(Plan, PlanningError) {
  // Ensure session is running
  use _ <- result.try(start_session(project_path, config))

  // Send the planning prompt to Claude
  let prompt = build_planning_prompt(feature_description)

  use _ <- result.try(
    send_prompt(prompt)
  )

  // Wait for Claude to finish and capture output
  use output <- result.try(
    wait_for_response(120)  // 2 minute timeout
  )

  // Parse the plan from the response
  parse_plan_response(output)
}

/// Review a plan and get improvement suggestions
///
/// Sends the review prompt to Claude and parses the feedback.
/// Returns whether the plan is approved and any suggestions.
pub fn review_plan(
  plan: Plan,
  pass: Int,
) -> Result(ReviewResult, PlanningError) {
  let prompt = build_review_prompt(plan, pass)

  use _ <- result.try(send_prompt(prompt))
  use output <- result.try(wait_for_response(60))

  parse_review_response(output)
}

/// Refine a plan based on review feedback
pub fn refine_plan(
  plan: Plan,
  feedback: ReviewResult,
) -> Result(Plan, PlanningError) {
  let prompt = build_refinement_prompt(plan, feedback)

  use _ <- result.try(send_prompt(prompt))
  use output <- result.try(wait_for_response(120))

  parse_plan_response(output)
}

/// Run the complete planning workflow
///
/// 1. Generate initial plan
/// 2. Review and refine (up to max_passes times)
/// 3. Generate beads from the final plan
pub fn run_planning_workflow(
  feature_description: String,
  project_path: String,
  config: Config,
  max_passes: Int,
) -> Result(List(String), PlanningError) {
  // 1. Generate initial plan
  use plan <- result.try(generate_plan(feature_description, project_path, config))

  // 2. Review and refine loop
  use final_plan <- result.try(
    review_loop(plan, 1, max_passes)
  )

  // 3. Create beads from the plan
  create_beads_from_plan(final_plan, project_path, config)
}

fn review_loop(
  plan: Plan,
  current_pass: Int,
  max_passes: Int,
) -> Result(Plan, PlanningError) {
  case current_pass > max_passes {
    True -> Ok(plan)  // Max passes reached, use current plan
    False -> {
      case review_plan(plan, current_pass) {
        Ok(review) -> {
          case review.approved {
            True -> Ok(plan)  // Plan approved
            False -> {
              // Refine and continue
              case refine_plan(plan, review) {
                Ok(refined) -> review_loop(refined, current_pass + 1, max_passes)
                Error(e) -> Error(e)
              }
            }
          }
        }
        Error(e) -> Error(e)
      }
    }
  }
}

// ============================================================================
// Beads Creation
// ============================================================================

/// Create beads from the finalized plan
pub fn create_beads_from_plan(
  plan: Plan,
  project_path: String,
  config: Config,
) -> Result(List(String), PlanningError) {
  // Track mapping from temp IDs to real bead IDs
  let id_mapping: List(#(String, String)) = []

  // 1. Create the epic first
  use epic_id <- result.try(
    beads.create(
      beads.CreateOptions(
        ..beads.default_create_options(),
        title: Some(plan.epic_title),
        description: Some(plan.epic_description),
        issue_type: Some(task.Epic),
        priority: Some(task.P1),
        design: Some(plan.summary),
      ),
      project_path,
      config,
    )
    |> result.map_error(BeadsError),
  )

  let created_ids = [epic_id]

  // 2. Create tasks without dependencies first
  let no_deps = list.filter(plan.tasks, fn(t) { list.is_empty(t.depends_on) })
  let with_deps = list.filter(plan.tasks, fn(t) { !list.is_empty(t.depends_on) })

  // Create tasks without deps
  use #(ids1, mapping1) <- result.try(
    create_tasks_batch(no_deps, epic_id, id_mapping, project_path, config)
  )

  // Create tasks with deps (in dependency order)
  use #(ids2, _mapping2) <- result.try(
    create_tasks_with_deps(with_deps, epic_id, mapping1, project_path, config)
  )

  Ok(list.concat([created_ids, ids1, ids2]))
}

fn create_tasks_batch(
  tasks: List(PlannedTask),
  epic_id: String,
  id_mapping: List(#(String, String)),
  project_path: String,
  config: Config,
) -> Result(#(List(String), List(#(String, String))), PlanningError) {
  list.try_fold(tasks, #([], id_mapping), fn(acc, task) {
    let #(ids, mapping) = acc

    // Create the bead
    case beads.create(
      beads.CreateOptions(
        ..beads.default_create_options(),
        title: Some(task.title),
        description: Some(task.description),
        issue_type: Some(task.issue_type),
        priority: Some(task.priority),
        design: task.design,
        acceptance: task.acceptance,
      ),
      project_path,
      config,
    ) {
      Ok(bead_id) -> {
        // Add parent-child relationship to epic
        case beads.add_child_to_epic(epic_id, bead_id, project_path, config) {
          Ok(_) -> {
            let new_mapping = [#(task.temp_id, bead_id), ..mapping]
            Ok(#([bead_id, ..ids], new_mapping))
          }
          Error(e) -> Error(BeadsError(e))
        }
      }
      Error(e) -> Error(BeadsError(e))
    }
  })
}

fn create_tasks_with_deps(
  tasks: List(PlannedTask),
  epic_id: String,
  id_mapping: List(#(String, String)),
  project_path: String,
  config: Config,
) -> Result(#(List(String), List(#(String, String))), PlanningError) {
  // Process in multiple passes until all are created or no progress
  create_tasks_with_deps_loop(tasks, [], epic_id, id_mapping, project_path, config, 10)
}

fn create_tasks_with_deps_loop(
  remaining: List(PlannedTask),
  created: List(String),
  epic_id: String,
  id_mapping: List(#(String, String)),
  project_path: String,
  config: Config,
  max_iterations: Int,
) -> Result(#(List(String), List(#(String, String))), PlanningError) {
  case remaining, max_iterations {
    [], _ -> Ok(#(created, id_mapping))
    _, 0 -> Ok(#(created, id_mapping))  // Give up on remaining
    _, _ -> {
      // Find tasks whose dependencies are all resolved
      let #(can_create, still_waiting) = list.partition(remaining, fn(task) {
        list.all(task.depends_on, fn(dep_id) {
          list.any(id_mapping, fn(entry) { entry.0 == dep_id })
        })
      })

      case can_create {
        [] -> Ok(#(created, id_mapping))  // No progress possible
        _ -> {
          // Create these tasks
          case create_tasks_with_deps_batch(can_create, epic_id, id_mapping, project_path, config) {
            Ok(#(new_ids, new_mapping)) -> {
              create_tasks_with_deps_loop(
                still_waiting,
                list.concat([created, new_ids]),
                epic_id,
                new_mapping,
                project_path,
                config,
                max_iterations - 1,
              )
            }
            Error(e) -> Error(e)
          }
        }
      }
    }
  }
}

fn create_tasks_with_deps_batch(
  tasks: List(PlannedTask),
  epic_id: String,
  id_mapping: List(#(String, String)),
  project_path: String,
  config: Config,
) -> Result(#(List(String), List(#(String, String))), PlanningError) {
  list.try_fold(tasks, #([], id_mapping), fn(acc, task) {
    let #(ids, mapping) = acc

    // Create the bead
    case beads.create(
      beads.CreateOptions(
        ..beads.default_create_options(),
        title: Some(task.title),
        description: Some(task.description),
        issue_type: Some(task.issue_type),
        priority: Some(task.priority),
        design: task.design,
        acceptance: task.acceptance,
      ),
      project_path,
      config,
    ) {
      Ok(bead_id) -> {
        // Add parent-child relationship to epic
        case beads.add_child_to_epic(epic_id, bead_id, project_path, config) {
          Ok(_) -> {
            // Add block dependencies
            case add_blocking_deps(bead_id, task.depends_on, mapping, project_path, config) {
              Ok(_) -> {
                let new_mapping = [#(task.temp_id, bead_id), ..mapping]
                Ok(#([bead_id, ..ids], new_mapping))
              }
              Error(e) -> Error(e)
            }
          }
          Error(e) -> Error(BeadsError(e))
        }
      }
      Error(e) -> Error(BeadsError(e))
    }
  })
}

fn add_blocking_deps(
  bead_id: String,
  depends_on: List(String),
  id_mapping: List(#(String, String)),
  project_path: String,
  config: Config,
) -> Result(Nil, PlanningError) {
  list.try_each(depends_on, fn(temp_id) {
    // Look up the real bead ID
    case list.find(id_mapping, fn(entry) { entry.0 == temp_id }) {
      Ok(#(_, real_id)) -> {
        beads.add_dependency(bead_id, real_id, task.Blocks, project_path, config)
        |> result.map_error(BeadsError)
      }
      Error(_) -> Ok(Nil)  // Dependency not found, skip
    }
  })
}

// ============================================================================
// Prompts
// ============================================================================

fn build_planning_prompt(feature_description: String) -> String {
  "I need you to create a detailed implementation plan for the following feature.

CRITICAL REQUIREMENTS:
1. **Small Tasks**: Each task should be completable in 30 minutes to 2 hours. If larger, split it.
2. **Independence**: Maximize tasks that can run in parallel without blocking each other.
3. **Clear Boundaries**: Each task should touch a distinct set of files to avoid merge conflicts.
4. **Explicit Dependencies**: Only add dependencies where truly necessary.
5. **Design Notes**: Include specific implementation guidance for each task.

Output your plan in this EXACT JSON format (no markdown, just raw JSON):

{
  \"epic_title\": \"Brief title for the epic\",
  \"epic_description\": \"Detailed description of the feature\",
  \"summary\": \"Brief summary of the implementation approach\",
  \"parallelization_score\": 75,
  \"tasks\": [
    {
      \"temp_id\": \"task-1\",
      \"title\": \"Concise task title\",
      \"description\": \"What this task accomplishes\",
      \"issue_type\": \"task\",
      \"priority\": 2,
      \"depends_on\": [],
      \"can_parallelize\": true,
      \"design\": \"Technical implementation notes\",
      \"acceptance\": \"How to verify completion\"
    }
  ]
}

Feature to plan:
" <> feature_description
}

fn build_review_prompt(plan: Plan, pass: Int) -> String {
  let plan_json = plan_to_json(plan)

  "Review this implementation plan (pass " <> int.to_string(pass) <> ").

Evaluate against these criteria:
1. **Task Size**: Are all tasks small enough (30min-2hr)? Flag any that are too large.
2. **Parallelization**: What percentage of tasks can run independently?
3. **Dependencies**: Are dependencies minimal and correct?
4. **Clarity**: Is each task's scope clear?
5. **Completeness**: Does the plan cover all aspects?

Output your review in this EXACT JSON format (no markdown):

{
  \"score\": 75,
  \"approved\": false,
  \"issues\": [\"issue 1\", \"issue 2\"],
  \"suggestions\": [\"suggestion 1\"],
  \"tasks_too_large\": [\"task-1\"],
  \"missing_dependencies\": []
}

Plan to review:
" <> plan_json
}

fn build_refinement_prompt(plan: Plan, feedback: ReviewResult) -> String {
  let plan_json = plan_to_json(plan)
  let feedback_json = review_to_json(feedback)

  "Refine this plan based on the review feedback.

Apply the suggested improvements while maintaining:
1. Maximum parallelization
2. Small, focused tasks (30min-2hr each)
3. Minimal, correct dependencies
4. Clear scope boundaries

Output the refined plan in the same JSON format as before.

Review feedback:
" <> feedback_json <> "

Current plan:
" <> plan_json
}

// ============================================================================
// Tmux Communication
// ============================================================================

fn send_prompt(prompt: String) -> Result(Nil, PlanningError) {
  let target = planning_session <> ":" <> planning_window

  // Escape the prompt for tmux send-keys
  let escaped = string.replace(prompt, "\"", "\\\"")
  let escaped = string.replace(escaped, "\n", " ")

  // Send to Claude
  tmux.send_keys(target, "\"" <> escaped <> "\" Enter")
  |> result.map_error(TmuxError)
}

fn wait_for_response(timeout_seconds: Int) -> Result(String, PlanningError) {
  // Poll for completion by checking if Claude is waiting for input
  wait_loop(timeout_seconds * 2, timeout_seconds * 2)
}

fn wait_loop(polls_remaining: Int, _max_polls: Int) -> Result(String, PlanningError) {
  case polls_remaining {
    0 -> Error(SessionNotReady("Timeout waiting for Claude response"))
    _ -> {
      // Sleep 500ms between polls
      shell.sleep_ms(500)

      // Capture pane output
      let target = planning_session <> ":" <> planning_window
      case tmux.capture_pane(target, 200) {
        Ok(output) -> {
          // Check if Claude is done (look for prompt or waiting indicator)
          case is_response_complete(output) {
            True -> Ok(output)
            False -> wait_loop(polls_remaining - 1, _max_polls)
          }
        }
        Error(e) -> Error(TmuxError(e))
      }
    }
  }
}

fn is_response_complete(output: String) -> Bool {
  // Claude Code shows specific patterns when waiting for input
  string.contains(output, "> ")
  || string.contains(output, "What would you like")
  || string.contains(output, "How can I help")
}

// ============================================================================
// JSON Parsing
// ============================================================================

pub type ReviewResult {
  ReviewResult(
    score: Int,
    approved: Bool,
    issues: List(String),
    suggestions: List(String),
    tasks_too_large: List(String),
  )
}

fn parse_plan_response(output: String) -> Result(Plan, PlanningError) {
  // Extract JSON from the response (it might have other text around it)
  case extract_json(output) {
    Some(json_str) -> {
      case json.parse(from: json_str, using: plan_decoder()) {
        Ok(plan) -> Ok(plan)
        Error(e) -> Error(ParseError("Failed to parse plan: " <> string.inspect(e)))
      }
    }
    None -> Error(ParseError("No valid JSON found in response"))
  }
}

fn parse_review_response(output: String) -> Result(ReviewResult, PlanningError) {
  case extract_json(output) {
    Some(json_str) -> {
      case json.parse(from: json_str, using: review_decoder()) {
        Ok(review) -> Ok(review)
        Error(e) -> Error(ParseError("Failed to parse review: " <> string.inspect(e)))
      }
    }
    None -> Error(ParseError("No valid JSON found in review response"))
  }
}

fn extract_json(text: String) -> Option(String) {
  // Find the first { and last } to extract JSON
  case string.split_once(text, "{") {
    Ok(#(_, rest)) -> {
      let json_candidate = "{" <> rest
      // Find matching closing brace (simplified - assumes well-formed JSON)
      case find_json_end(json_candidate) {
        Some(end_idx) -> Some(string.slice(json_candidate, 0, end_idx + 1))
        None -> None
      }
    }
    Error(_) -> None
  }
}

fn find_json_end(text: String) -> Option(Int) {
  find_json_end_loop(text, 0, 0, 0)
}

fn find_json_end_loop(text: String, index: Int, depth: Int, last_close: Int) -> Option(Int) {
  case string.pop_grapheme(string.slice(text, index, string.length(text))) {
    Ok(#(char, _)) -> {
      let new_depth = case char {
        "{" -> depth + 1
        "}" -> depth - 1
        _ -> depth
      }
      let new_last = case char {
        "}" -> index
        _ -> last_close
      }
      case new_depth {
        0 -> Some(new_last)
        _ -> find_json_end_loop(text, index + 1, new_depth, new_last)
      }
    }
    Error(_) -> {
      case last_close > 0 {
        True -> Some(last_close)
        False -> None
      }
    }
  }
}

fn plan_decoder() -> decode.Decoder(Plan) {
  use epic_title <- decode.field("epic_title", decode.string)
  use epic_description <- decode.field("epic_description", decode.string)
  use summary <- decode.field("summary", decode.string)
  use parallelization_score <- decode.optional_field("parallelization_score", 50, decode.int)
  use tasks <- decode.field("tasks", decode.list(planned_task_decoder()))

  decode.success(Plan(
    epic_title:,
    epic_description:,
    summary:,
    tasks:,
    parallelization_score:,
  ))
}

fn planned_task_decoder() -> decode.Decoder(PlannedTask) {
  use temp_id <- decode.field("temp_id", decode.string)
  use title <- decode.field("title", decode.string)
  use description <- decode.field("description", decode.string)
  use issue_type_str <- decode.optional_field("issue_type", "task", decode.string)
  use priority_int <- decode.optional_field("priority", 2, decode.int)
  use depends_on <- decode.optional_field("depends_on", [], decode.list(decode.string))
  use can_parallelize <- decode.optional_field("can_parallelize", True, decode.bool)
  use design <- decode.optional_field("design", None, decode.optional(decode.string))
  use acceptance <- decode.optional_field("acceptance", None, decode.optional(decode.string))

  decode.success(PlannedTask(
    temp_id:,
    title:,
    description:,
    issue_type: task.issue_type_from_string(issue_type_str),
    priority: task.priority_from_int(priority_int),
    depends_on:,
    can_parallelize:,
    design:,
    acceptance:,
  ))
}

fn review_decoder() -> decode.Decoder(ReviewResult) {
  use score <- decode.field("score", decode.int)
  use approved <- decode.field("approved", decode.bool)
  use issues <- decode.optional_field("issues", [], decode.list(decode.string))
  use suggestions <- decode.optional_field("suggestions", [], decode.list(decode.string))
  use tasks_too_large <- decode.optional_field("tasks_too_large", [], decode.list(decode.string))

  decode.success(ReviewResult(
    score:,
    approved:,
    issues:,
    suggestions:,
    tasks_too_large:,
  ))
}

// ============================================================================
// JSON Encoding (for prompts)
// ============================================================================

fn plan_to_json(plan: Plan) -> String {
  let tasks_json = list.map(plan.tasks, task_to_json) |> string.join(", ")

  "{\"epic_title\": \"" <> escape_json_string(plan.epic_title) <> "\", "
  <> "\"epic_description\": \"" <> escape_json_string(plan.epic_description) <> "\", "
  <> "\"summary\": \"" <> escape_json_string(plan.summary) <> "\", "
  <> "\"parallelization_score\": " <> int.to_string(plan.parallelization_score) <> ", "
  <> "\"tasks\": [" <> tasks_json <> "]}"
}

fn task_to_json(task: PlannedTask) -> String {
  let deps_json = list.map(task.depends_on, fn(d) { "\"" <> d <> "\"" }) |> string.join(", ")
  let design_json = case task.design {
    Some(d) -> "\"" <> escape_json_string(d) <> "\""
    None -> "null"
  }
  let acceptance_json = case task.acceptance {
    Some(a) -> "\"" <> escape_json_string(a) <> "\""
    None -> "null"
  }

  "{\"temp_id\": \"" <> task.temp_id <> "\", "
  <> "\"title\": \"" <> escape_json_string(task.title) <> "\", "
  <> "\"description\": \"" <> escape_json_string(task.description) <> "\", "
  <> "\"issue_type\": \"" <> task.issue_type_to_string(task.issue_type) <> "\", "
  <> "\"priority\": " <> int.to_string(task.priority_to_int(task.priority)) <> ", "
  <> "\"depends_on\": [" <> deps_json <> "], "
  <> "\"can_parallelize\": " <> bool_to_json(task.can_parallelize) <> ", "
  <> "\"design\": " <> design_json <> ", "
  <> "\"acceptance\": " <> acceptance_json <> "}"
}

fn review_to_json(review: ReviewResult) -> String {
  let issues_json = list.map(review.issues, fn(i) { "\"" <> escape_json_string(i) <> "\"" }) |> string.join(", ")
  let suggestions_json = list.map(review.suggestions, fn(s) { "\"" <> escape_json_string(s) <> "\"" }) |> string.join(", ")
  let tasks_json = list.map(review.tasks_too_large, fn(t) { "\"" <> t <> "\"" }) |> string.join(", ")

  "{\"score\": " <> int.to_string(review.score) <> ", "
  <> "\"approved\": " <> bool_to_json(review.approved) <> ", "
  <> "\"issues\": [" <> issues_json <> "], "
  <> "\"suggestions\": [" <> suggestions_json <> "], "
  <> "\"tasks_too_large\": [" <> tasks_json <> "]}"
}

fn escape_json_string(s: String) -> String {
  s
  |> string.replace("\\", "\\\\")
  |> string.replace("\"", "\\\"")
  |> string.replace("\n", "\\n")
  |> string.replace("\r", "\\r")
  |> string.replace("\t", "\\t")
}

fn bool_to_json(b: Bool) -> String {
  case b {
    True -> "true"
    False -> "false"
  }
}
