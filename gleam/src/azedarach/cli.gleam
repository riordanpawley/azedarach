// CLI argument parsing

import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/result
import gleam/string

pub type Command {
  Run(project_path: Option(String))
  Help
  Version
}

pub fn parse(args: List(String)) -> Result(Command, String) {
  case args {
    [] -> Ok(Run(None))
    ["--help"] | ["-h"] -> Ok(Help)
    ["--version"] | ["-v"] -> Ok(Version)
    [path] -> Ok(Run(Some(path)))
    [path, "--help"] | [path, "-h"] -> Ok(Help)
    _ -> Error("Unknown arguments: " <> string.join(args, " "))
  }
}

pub fn help_text() -> String {
  "azedarach - TUI Kanban for Claude Code Sessions

USAGE:
    azedarach [PROJECT_PATH]

ARGUMENTS:
    PROJECT_PATH    Path to project directory (default: current directory)

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

For full documentation, see docs/README.md"
}
