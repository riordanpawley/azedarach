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

// ============================================================================
// Encoders
// ============================================================================

pub fn encode_config(cfg: Config) -> String {
  json.to_string(config_to_json(cfg))
}

fn config_to_json(cfg: Config) -> json.Json {
  json.object([
    #("worktree", worktree_to_json(cfg.worktree)),
    #("session", session_to_json(cfg.session)),
    #("devServer", dev_server_to_json(cfg.dev_server)),
    #("git", git_to_json(cfg.git)),
    #("pr", pr_to_json(cfg.pr)),
    #("beads", beads_to_json(cfg.beads)),
    #("polling", polling_to_json(cfg.polling)),
    #("theme", json.string(cfg.theme)),
  ])
}

fn worktree_to_json(wt: WorktreeConfig) -> json.Json {
  json.object([
    #("pathTemplate", json.string(wt.path_template)),
    #("initCommands", json.array(wt.init_commands, json.string)),
    #("continueOnFailure", json.bool(wt.continue_on_failure)),
  ])
}

fn session_to_json(sess: SessionConfig) -> json.Json {
  json.object([
    #("shell", json.string(sess.shell)),
    #("tmuxPrefix", json.string(sess.tmux_prefix)),
    #("backgroundTasks", json.array(sess.background_tasks, json.string)),
  ])
}

fn dev_server_to_json(ds: DevServerConfig) -> json.Json {
  json.object([
    #("servers", json.array(ds.servers, server_definition_to_json)),
  ])
}

fn server_definition_to_json(sd: ServerDefinition) -> json.Json {
  json.object([
    #("name", json.string(sd.name)),
    #("command", json.string(sd.command)),
    #("ports", json.array(sd.ports, port_to_json)),
  ])
}

fn port_to_json(port: #(String, Int)) -> json.Json {
  json.object([
    #("name", json.string(port.0)),
    #("port", json.int(port.1)),
  ])
}

fn git_to_json(git: GitConfig) -> json.Json {
  json.object([
    #("workflowMode", workflow_mode_to_json(git.workflow_mode)),
    #("pushBranchOnCreate", json.bool(git.push_branch_on_create)),
    #("pushEnabled", json.bool(git.push_enabled)),
    #("fetchEnabled", json.bool(git.fetch_enabled)),
    #("baseBranch", json.string(git.base_branch)),
    #("remote", json.string(git.remote)),
    #("branchPrefix", json.string(git.branch_prefix)),
  ])
}

fn workflow_mode_to_json(mode: WorkflowMode) -> json.Json {
  case mode {
    Local -> json.string("local")
    Origin -> json.string("origin")
  }
}

fn pr_to_json(pr: PrConfig) -> json.Json {
  json.object([
    #("enabled", json.bool(pr.enabled)),
    #("autoDraft", json.bool(pr.auto_draft)),
    #("autoMerge", json.bool(pr.auto_merge)),
  ])
}

fn beads_to_json(beads: BeadsConfig) -> json.Json {
  json.object([
    #("syncEnabled", json.bool(beads.sync_enabled)),
  ])
}

fn polling_to_json(polling: PollingConfig) -> json.Json {
  json.object([
    #("beadsRefresh", json.int(polling.beads_refresh_ms)),
    #("sessionMonitor", json.int(polling.session_monitor_ms)),
  ])
}

// ============================================================================
// Save
// ============================================================================

/// Save config to project path
pub fn save(cfg: Config, project_path: Option(String)) -> Result(Nil, ConfigError) {
  let base_path = option.unwrap(project_path, ".")
  let config_path = base_path <> "/.azedarach.json"
  let json_string = encode_config(cfg)

  case simplifile.write(config_path, json_string) {
    Ok(_) -> Ok(Nil)
    Error(_) -> Error(ParseError("Failed to write config to " <> config_path))
  }
}

// ============================================================================
// Editable Settings
// ============================================================================

/// Definition of an editable setting
pub type SettingDefinition {
  SettingDefinition(
    key: String,
    label: String,
    get_value: fn(Config) -> SettingValue,
    toggle: fn(Config) -> Config,
  )
}

/// Setting value can be bool or string
pub type SettingValue {
  BoolValue(Bool)
  StringValue(String)
}

/// Format a setting value for display
pub fn setting_value_to_string(value: SettingValue) -> String {
  case value {
    BoolValue(True) -> "yes"
    BoolValue(False) -> "no"
    StringValue(s) -> s
  }
}

/// All editable settings - matching TypeScript EDITABLE_SETTINGS
pub fn editable_settings() -> List(SettingDefinition) {
  [
    SettingDefinition(
      key: "pushBranchOnCreate",
      label: "Push on Create",
      get_value: fn(c) { BoolValue(c.git.push_branch_on_create) },
      toggle: fn(c) {
        Config(
          ..c,
          git: GitConfig(..c.git, push_branch_on_create: !c.git.push_branch_on_create),
        )
      },
    ),
    SettingDefinition(
      key: "pushEnabled",
      label: "Git Push",
      get_value: fn(c) { BoolValue(c.git.push_enabled) },
      toggle: fn(c) {
        Config(..c, git: GitConfig(..c.git, push_enabled: !c.git.push_enabled))
      },
    ),
    SettingDefinition(
      key: "fetchEnabled",
      label: "Git Fetch",
      get_value: fn(c) { BoolValue(c.git.fetch_enabled) },
      toggle: fn(c) {
        Config(..c, git: GitConfig(..c.git, fetch_enabled: !c.git.fetch_enabled))
      },
    ),
    SettingDefinition(
      key: "prEnabled",
      label: "PR Enabled",
      get_value: fn(c) { BoolValue(c.pr.enabled) },
      toggle: fn(c) {
        Config(..c, pr: PrConfig(..c.pr, enabled: !c.pr.enabled))
      },
    ),
    SettingDefinition(
      key: "autoDraft",
      label: "Auto Draft PR",
      get_value: fn(c) { BoolValue(c.pr.auto_draft) },
      toggle: fn(c) {
        Config(..c, pr: PrConfig(..c.pr, auto_draft: !c.pr.auto_draft))
      },
    ),
    SettingDefinition(
      key: "autoMerge",
      label: "Auto Merge PR",
      get_value: fn(c) { BoolValue(c.pr.auto_merge) },
      toggle: fn(c) {
        Config(..c, pr: PrConfig(..c.pr, auto_merge: !c.pr.auto_merge))
      },
    ),
    SettingDefinition(
      key: "beadsSyncEnabled",
      label: "Beads Sync",
      get_value: fn(c) { BoolValue(c.beads.sync_enabled) },
      toggle: fn(c) {
        Config(..c, beads: BeadsConfig(sync_enabled: !c.beads.sync_enabled))
      },
    ),
    SettingDefinition(
      key: "workflowMode",
      label: "Workflow Mode",
      get_value: fn(c) {
        case c.git.workflow_mode {
          Local -> StringValue("local")
          Origin -> StringValue("origin")
        }
      },
      toggle: fn(c) {
        let new_mode = case c.git.workflow_mode {
          Local -> Origin
          Origin -> Local
        }
        Config(..c, git: GitConfig(..c.git, workflow_mode: new_mode))
      },
    ),
    SettingDefinition(
      key: "theme",
      label: "Theme",
      get_value: fn(c) { StringValue(c.theme) },
      toggle: fn(c) {
        // Cycle between available themes
        let new_theme = case c.theme {
          "catppuccin-macchiato" -> "catppuccin-mocha"
          "catppuccin-mocha" -> "catppuccin-latte"
          "catppuccin-latte" -> "catppuccin-frappe"
          _ -> "catppuccin-macchiato"
        }
        Config(..c, theme: new_theme)
      },
    ),
  ]
}
