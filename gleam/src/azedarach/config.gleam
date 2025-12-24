// Configuration loading and schema

import gleam/dynamic.{type Dynamic}
import gleam/json
import gleam/option.{type Option, None, Some}
import gleam/result
import gleam/string
import simplifile

// Main config type
pub type Config {
  Config(
    worktree: WorktreeConfig,
    session: SessionConfig,
    dev_server: DevServerConfig,
    git: GitConfig,
    pr: PrConfig,
    beads: BeadsConfig,
    polling: PollingConfig,
    theme: String,
  )
}

pub type WorktreeConfig {
  WorktreeConfig(
    path_template: String,
    init_commands: List(String),
    continue_on_failure: Bool,
  )
}

pub type SessionConfig {
  SessionConfig(
    shell: String,
    tmux_prefix: String,
    background_tasks: List(String),
  )
}

pub type DevServerConfig {
  DevServerConfig(servers: List(ServerDefinition))
}

pub type ServerDefinition {
  ServerDefinition(
    name: String,
    command: String,
    ports: List(#(String, Int)),
  )
}

pub type GitConfig {
  GitConfig(
    workflow_mode: WorkflowMode,
    push_branch_on_create: Bool,
    push_enabled: Bool,
    fetch_enabled: Bool,
    base_branch: String,
    remote: String,
    branch_prefix: String,
  )
}

pub type WorkflowMode {
  Local
  Origin
}

pub type PrConfig {
  PrConfig(enabled: Bool, auto_draft: Bool, auto_merge: Bool)
}

pub type BeadsConfig {
  BeadsConfig(sync_enabled: Bool)
}

pub type PollingConfig {
  PollingConfig(beads_refresh_ms: Int, session_monitor_ms: Int)
}

pub type ConfigError {
  FileNotFound(path: String)
  ParseError(message: String)
  ValidationError(message: String)
}

pub fn error_to_string(err: ConfigError) -> String {
  case err {
    FileNotFound(path) -> "Config file not found: " <> path
    ParseError(msg) -> "Failed to parse config: " <> msg
    ValidationError(msg) -> "Invalid config: " <> msg
  }
}

pub fn load(project_path: Option(String)) -> Result(Config, ConfigError) {
  let base_path = option.unwrap(project_path, ".")
  let config_path = base_path <> "/.azedarach.json"

  case simplifile.read(config_path) {
    Ok(content) -> parse_config(content)
    Error(_) -> Ok(default_config())
  }
}

pub fn default_config() -> Config {
  Config(
    worktree: WorktreeConfig(
      path_template: "../{project}-{bead-id}",
      init_commands: ["direnv allow"],
      continue_on_failure: True,
    ),
    session: SessionConfig(
      shell: "zsh",
      tmux_prefix: "C-a",
      background_tasks: [],
    ),
    dev_server: DevServerConfig(servers: [
      ServerDefinition(
        name: "default",
        command: "npm run dev",
        ports: [#("PORT", 3000)],
      ),
    ]),
    git: GitConfig(
      workflow_mode: Origin,
      push_branch_on_create: True,
      push_enabled: True,
      fetch_enabled: True,
      base_branch: "main",
      remote: "origin",
      branch_prefix: "az-",
    ),
    pr: PrConfig(enabled: True, auto_draft: True, auto_merge: False),
    beads: BeadsConfig(sync_enabled: True),
    polling: PollingConfig(beads_refresh_ms: 30_000, session_monitor_ms: 500),
    theme: "catppuccin-macchiato",
  )
}

fn parse_config(content: String) -> Result(Config, ConfigError) {
  case json.decode(content, config_decoder()) {
    Ok(cfg) -> Ok(cfg)
    Error(e) -> Error(ParseError(string.inspect(e)))
  }
}

fn config_decoder() -> fn(Dynamic) -> Result(Config, List(dynamic.DecodeError)) {
  dynamic.decode8(
    Config,
    dynamic.optional_field("worktree", worktree_decoder())
      |> with_default(default_config().worktree),
    dynamic.optional_field("session", session_decoder())
      |> with_default(default_config().session),
    dynamic.optional_field("devServer", dev_server_decoder())
      |> with_default(default_config().dev_server),
    dynamic.optional_field("git", git_decoder())
      |> with_default(default_config().git),
    dynamic.optional_field("pr", pr_decoder())
      |> with_default(default_config().pr),
    dynamic.optional_field("beads", beads_decoder())
      |> with_default(default_config().beads),
    dynamic.optional_field("polling", polling_decoder())
      |> with_default(default_config().polling),
    dynamic.optional_field("theme", dynamic.string)
      |> with_default(default_config().theme),
  )
}

fn with_default(
  decoder: fn(Dynamic) -> Result(Option(a), List(dynamic.DecodeError)),
  default: a,
) -> fn(Dynamic) -> Result(a, List(dynamic.DecodeError)) {
  fn(dyn) {
    case decoder(dyn) {
      Ok(Some(value)) -> Ok(value)
      Ok(None) -> Ok(default)
      Error(_) -> Ok(default)
    }
  }
}

fn worktree_decoder() -> fn(Dynamic) ->
  Result(WorktreeConfig, List(dynamic.DecodeError)) {
  dynamic.decode3(
    WorktreeConfig,
    dynamic.field("pathTemplate", dynamic.string),
    dynamic.field("initCommands", dynamic.list(dynamic.string)),
    dynamic.optional_field("continueOnFailure", dynamic.bool)
      |> with_default(True),
  )
}

fn session_decoder() -> fn(Dynamic) ->
  Result(SessionConfig, List(dynamic.DecodeError)) {
  dynamic.decode3(
    SessionConfig,
    dynamic.optional_field("shell", dynamic.string) |> with_default("zsh"),
    dynamic.optional_field("tmuxPrefix", dynamic.string) |> with_default("C-a"),
    dynamic.optional_field("backgroundTasks", dynamic.list(dynamic.string))
      |> with_default([]),
  )
}

fn dev_server_decoder() -> fn(Dynamic) ->
  Result(DevServerConfig, List(dynamic.DecodeError)) {
  fn(dyn) {
    // For now, use default - full implementation would parse servers object
    Ok(default_config().dev_server)
  }
}

fn git_decoder() -> fn(Dynamic) -> Result(GitConfig, List(dynamic.DecodeError)) {
  dynamic.decode7(
    GitConfig,
    dynamic.field("workflowMode", workflow_mode_decoder()),
    dynamic.optional_field("pushBranchOnCreate", dynamic.bool)
      |> with_default(True),
    dynamic.optional_field("pushEnabled", dynamic.bool) |> with_default(True),
    dynamic.optional_field("fetchEnabled", dynamic.bool) |> with_default(True),
    dynamic.optional_field("baseBranch", dynamic.string) |> with_default("main"),
    dynamic.optional_field("remote", dynamic.string) |> with_default("origin"),
    dynamic.optional_field("branchPrefix", dynamic.string)
      |> with_default("az-"),
  )
}

fn workflow_mode_decoder() -> fn(Dynamic) ->
  Result(WorkflowMode, List(dynamic.DecodeError)) {
  fn(dyn) {
    case dynamic.string(dyn) {
      Ok("local") -> Ok(Local)
      Ok("origin") -> Ok(Origin)
      Ok(_) -> Ok(Origin)
      Error(e) -> Error(e)
    }
  }
}

fn pr_decoder() -> fn(Dynamic) -> Result(PrConfig, List(dynamic.DecodeError)) {
  dynamic.decode3(
    PrConfig,
    dynamic.optional_field("enabled", dynamic.bool) |> with_default(True),
    dynamic.optional_field("autoDraft", dynamic.bool) |> with_default(True),
    dynamic.optional_field("autoMerge", dynamic.bool) |> with_default(False),
  )
}

fn beads_decoder() -> fn(Dynamic) ->
  Result(BeadsConfig, List(dynamic.DecodeError)) {
  dynamic.decode1(
    BeadsConfig,
    dynamic.optional_field("syncEnabled", dynamic.bool) |> with_default(True),
  )
}

fn polling_decoder() -> fn(Dynamic) ->
  Result(PollingConfig, List(dynamic.DecodeError)) {
  dynamic.decode2(
    PollingConfig,
    dynamic.optional_field("beadsRefresh", dynamic.int)
      |> with_default(30_000),
    dynamic.optional_field("sessionMonitor", dynamic.int) |> with_default(500),
  )
}
