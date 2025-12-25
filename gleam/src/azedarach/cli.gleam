// CLI argument parsing
//
// Commands:
//   az                       Launch TUI (default)
//   az add <path> [--name]   Register a project
//   az list                  List registered projects
//   az project add/list/remove/switch
//   az --help                Show help

import gleam/io
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/string
import azedarach/services/project_service

pub type Command {
  // TUI launch
  Run(project_path: Option(String))
  // Project management
  ProjectAdd(path: String, name: Option(String))
  ProjectList
  ProjectRemove(name: String)
  ProjectSwitch(name: String)
  // Help/Version
  Help
  Version
}

pub fn parse(args: List(String)) -> Result(Command, String) {
  case args {
    // No args - launch TUI
    [] -> Ok(Run(None))

    // Help/Version
    ["--help"] | ["-h"] -> Ok(Help)
    ["--version"] | ["-v"] -> Ok(Version)

    // Top-level shortcuts
    ["add", path] -> Ok(ProjectAdd(path, None))
    ["add", path, "--name", name] -> Ok(ProjectAdd(path, Some(name)))
    ["add", path, "-n", name] -> Ok(ProjectAdd(path, Some(name)))
    ["list"] -> Ok(ProjectList)

    // Project subcommands
    ["project", "add", path] -> Ok(ProjectAdd(path, None))
    ["project", "add", path, "--name", name] -> Ok(ProjectAdd(path, Some(name)))
    ["project", "add", path, "-n", name] -> Ok(ProjectAdd(path, Some(name)))
    ["project", "list"] -> Ok(ProjectList)
    ["project", "remove", name] -> Ok(ProjectRemove(name))
    ["project", "switch", name] -> Ok(ProjectSwitch(name))
    ["project"] -> Ok(Help)
    ["project", "--help"] -> Ok(Help)

    // TUI with path
    [path] if !string.starts_with(path, "-") -> Ok(Run(Some(path)))
    [path, "--help"] | [path, "-h"] -> Ok(Help)

    _ -> Error("Unknown arguments: " <> string.join(args, " "))
  }
}

/// Execute a CLI command
pub fn execute(cmd: Command) -> Result(Nil, String) {
  case cmd {
    Run(_path) -> {
      // TUI launch is handled by main
      Ok(Nil)
    }

    ProjectAdd(path, name) -> {
      case project_service.add_project(path, name) {
        Ok(entry) -> {
          io.println("✓ Project '" <> entry.name <> "' added successfully.")
          io.println("  Path: " <> entry.path)
          Ok(Nil)
        }
        Error(e) -> Error(project_service.error_to_string(e))
      }
    }

    ProjectList -> {
      case project_service.load_registry() {
        Ok(config) -> {
          case config.projects {
            [] -> {
              io.println("No projects registered.")
              io.println("Use 'az add <path>' to register a project.")
              Ok(Nil)
            }
            projects -> {
              io.println("Registered projects:")
              io.println("")
              list.each(projects, fn(p) {
                let is_current = config.default_project == Some(p.name)
                io.println(project_service.format_entry(p, is_current))
                io.println("    Path: " <> p.path)
                case is_current {
                  True -> io.println("    (current)")
                  False -> Nil
                }
                io.println("")
              })
              Ok(Nil)
            }
          }
        }
        Error(e) -> Error(project_service.error_to_string(e))
      }
    }

    ProjectRemove(name) -> {
      case project_service.remove_project(name) {
        Ok(_) -> {
          io.println("✓ Project '" <> name <> "' removed successfully.")
          Ok(Nil)
        }
        Error(e) -> Error(project_service.error_to_string(e))
      }
    }

    ProjectSwitch(name) -> {
      case project_service.switch_project(name) {
        Ok(entry) -> {
          io.println(
            "✓ Switched to project '" <> entry.name <> "' and set as default.",
          )
          Ok(Nil)
        }
        Error(e) -> Error(project_service.error_to_string(e))
      }
    }

    Help -> {
      io.println(help_text())
      Ok(Nil)
    }

    Version -> {
      io.println("azedarach 0.1.0")
      Ok(Nil)
    }
  }
}

pub fn help_text() -> String {
  "azedarach - TUI Kanban for Claude Code Sessions

USAGE:
    az                          Launch TUI
    az [PROJECT_PATH]           Launch TUI for specific project
    az add <path> [--name NAME] Register a new project
    az list                     List registered projects
    az project <subcommand>     Project management

PROJECT SUBCOMMANDS:
    az project add <path>       Register a new project
    az project list             List registered projects
    az project remove <name>    Unregister a project
    az project switch <name>    Switch to a project (sets as default)

OPTIONS:
    -h, --help      Print help information
    -v, --version   Print version information

KEYBINDINGS:
    hjkl / arrows   Navigate
    Space           Open action menu
    Enter           View details / enter epic
    /               Search
    f               Filter menu
    ,               Sort menu
    ?               Help overlay
    q               Quit

CONFIG:
    Projects are stored in ~/.config/azedarach/projects.json

For full documentation, see docs/README.md"
}
