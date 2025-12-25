// Configuration loading and schema

import gleam/decode.{type Decoder}
import gleam/json
import gleam/option.{type Option, None, Some}
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
  case json.parse(from: content, using: config_decoder()) {
    Ok(cfg) -> Ok(cfg)
    Error(e) -> Error(ParseError(string.inspect(e)))
  }
}

// ============================================================================
// Decoders
// ============================================================================

fn config_decoder() -> Decoder(Config) {
  let defaults = default_config()

  use worktree <- decode.optional_field(
    "worktree",
    worktree_decoder(),
    defaults.worktree,
  )
  use session <- decode.optional_field(
    "session",
    session_decoder(),
    defaults.session,
  )
  use dev_server <- decode.optional_field(
    "devServer",
    dev_server_decoder(),
    defaults.dev_server,
  )
  use git <- decode.optional_field("git", git_decoder(), defaults.git)
  use pr <- decode.optional_field("pr", pr_decoder(), defaults.pr)
  use beads <- decode.optional_field("beads", beads_decoder(), defaults.beads)
  use polling <- decode.optional_field(
    "polling",
    polling_decoder(),
    defaults.polling,
  )
  use theme <- decode.optional_field("theme", decode.string, defaults.theme)

  decode.success(Config(
    worktree:,
    session:,
    dev_server:,
    git:,
    pr:,
    beads:,
    polling:,
    theme:,
  ))
}

fn worktree_decoder() -> Decoder(WorktreeConfig) {
  use path_template <- decode.field("pathTemplate", decode.string)
  use init_commands <- decode.field("initCommands", decode.list(decode.string))
  use continue_on_failure <- decode.optional_field(
    "continueOnFailure",
    decode.bool,
    True,
  )

  decode.success(WorktreeConfig(path_template:, init_commands:, continue_on_failure:))
}

fn session_decoder() -> Decoder(SessionConfig) {
  use shell <- decode.optional_field("shell", decode.string, "zsh")
  use tmux_prefix <- decode.optional_field("tmuxPrefix", decode.string, "C-a")
  use background_tasks <- decode.optional_field(
    "backgroundTasks",
    decode.list(decode.string),
    [],
  )

  decode.success(SessionConfig(shell:, tmux_prefix:, background_tasks:))
}

fn dev_server_decoder() -> Decoder(DevServerConfig) {
  use servers <- decode.optional_field(
    "servers",
    decode.list(server_definition_decoder()),
    default_config().dev_server.servers,
  )

  decode.success(DevServerConfig(servers:))
}

fn server_definition_decoder() -> Decoder(ServerDefinition) {
  use name <- decode.field("name", decode.string)
  use command <- decode.field("command", decode.string)
  use ports <- decode.optional_field(
    "ports",
    decode.list(port_decoder()),
    [#("PORT", 3000)],
  )

  decode.success(ServerDefinition(name:, command:, ports:))
}

fn port_decoder() -> Decoder(#(String, Int)) {
  use name <- decode.field("name", decode.string)
  use port <- decode.field("port", decode.int)
  decode.success(#(name, port))
}

fn git_decoder() -> Decoder(GitConfig) {
  use workflow_mode <- decode.optional_field(
    "workflowMode",
    workflow_mode_decoder(),
    Origin,
  )
  use push_branch_on_create <- decode.optional_field(
    "pushBranchOnCreate",
    decode.bool,
    True,
  )
  use push_enabled <- decode.optional_field("pushEnabled", decode.bool, True)
  use fetch_enabled <- decode.optional_field("fetchEnabled", decode.bool, True)
  use base_branch <- decode.optional_field("baseBranch", decode.string, "main")
  use remote <- decode.optional_field("remote", decode.string, "origin")
  use branch_prefix <- decode.optional_field(
    "branchPrefix",
    decode.string,
    "az-",
  )

  decode.success(GitConfig(
    workflow_mode:,
    push_branch_on_create:,
    push_enabled:,
    fetch_enabled:,
    base_branch:,
    remote:,
    branch_prefix:,
  ))
}

fn workflow_mode_decoder() -> Decoder(WorkflowMode) {
  use mode <- decode.then(decode.string)
  case mode {
    "local" -> decode.success(Local)
    "origin" -> decode.success(Origin)
    _ -> decode.success(Origin)
  }
}

fn pr_decoder() -> Decoder(PrConfig) {
  use enabled <- decode.optional_field("enabled", decode.bool, True)
  use auto_draft <- decode.optional_field("autoDraft", decode.bool, True)
  use auto_merge <- decode.optional_field("autoMerge", decode.bool, False)

  decode.success(PrConfig(enabled:, auto_draft:, auto_merge:))
}

fn beads_decoder() -> Decoder(BeadsConfig) {
  use sync_enabled <- decode.optional_field("syncEnabled", decode.bool, True)

  decode.success(BeadsConfig(sync_enabled:))
}

fn polling_decoder() -> Decoder(PollingConfig) {
  use beads_refresh_ms <- decode.optional_field(
    "beadsRefresh",
    decode.int,
    30_000,
  )
  use session_monitor_ms <- decode.optional_field(
    "sessionMonitor",
    decode.int,
    500,
  )

  decode.success(PollingConfig(beads_refresh_ms:, session_monitor_ms:))
}
