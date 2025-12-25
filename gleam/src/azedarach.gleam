// Azedarach - TUI Kanban for Claude Code Sessions
// Entry point with OTP supervision tree

import argv
import gleam/io
import gleam/result
import gleam/erlang/process
import azedarach/cli
import azedarach/config
import azedarach/ui/app
import azedarach/actors/app_supervisor

pub fn main() {
  let args = argv.load().arguments

  case cli.parse(args) {
    Ok(cli.Run(project_path)) -> {
      case config.load(project_path) {
        Ok(cfg) -> start_with_supervision(cfg)
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

/// Start the application with OTP supervision tree
fn start_with_supervision(cfg: config.Config) -> Nil {
  case app_supervisor.start(cfg) {
    Ok(context) -> {
      // Initialize coordinator (starts beads refresh, project discovery)
      app_supervisor.initialize_coordinator(context)

      // Start the TUI
      app.start_with_context(context)

      // Keep the main process alive
      // The TUI will handle shutdown
      process.sleep_forever()
    }
    Error(e) -> {
      io.println_error("Failed to start application: " <> app_supervisor.error_to_string(e))
      halt(1)
    }
  }
}

@external(erlang, "erlang", "halt")
fn halt(code: Int) -> Nil
