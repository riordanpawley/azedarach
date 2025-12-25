// Azedarach - TUI Kanban for Claude Code Sessions
// Entry point

import argv
import gleam/io
import gleam/result
import azedarach/cli
import azedarach/config
import azedarach/ui/app

pub fn main() {
  let args = argv.load().arguments

  case cli.parse(args) {
    Ok(cli.Run(project_path)) -> {
      case config.load(project_path) {
        Ok(cfg) -> app.start(cfg)
        Error(e) -> {
          io.println_error("Failed to load config: " <> config.error_to_string(e))
          halt(1)
        }
      }
    }

    Ok(cli.Help) -> {
      io.println(cli.help_text())
    }

    Ok(cli.Version) -> {
      io.println("azedarach 1.0.0")
    }

    Error(e) -> {
      io.println_error("Error: " <> e)
      io.println_error("")
      io.println_error(cli.help_text())
      halt(1)
    }
  }
}

@external(erlang, "erlang", "halt")
fn halt(code: Int) -> Nil
