// Azedarach - TUI Kanban for Claude Code Sessions
// Entry point with OTP supervision tree

import argv
import gleam/io
import gleam/string
import azedarach/cli
import azedarach/config
import azedarach/ui/app
import azedarach/actors/app_supervisor
import azedarach/util/logger

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

    // Handle other CLI commands (session, project, hooks, etc.)
    Ok(cmd) -> {
      case cli.execute(cmd) {
        Ok(_) -> Nil
        Error(e) -> {
          io.println_error("Error: " <> e)
          halt(1)
        }
      }
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
  // Initialize logging
  logger.init()
  logger.info("Azedarach starting...")
  logger.info("Config loaded")

  case app_supervisor.start(cfg) {
    Ok(context) -> {
      logger.info("Supervisor started successfully")

      // Initialize coordinator (starts beads refresh, project discovery)
      logger.info("Initializing coordinator...")
      app_supervisor.initialize_coordinator(context)
      logger.info("Coordinator initialized")

      // Start the TUI (now returns Result)
      logger.info("Starting TUI - about to call app.start_with_context...")
      // Note: If Shore crashes with let_assert, this won't catch it
      // The crash happens inside shore.start() before returning
      case app.start_with_context(context) {
        Ok(_) -> {
          logger.info("TUI exited normally")
          Nil
        }
        Error(e) -> {
          logger.error_ctx("Failed to start TUI (returned Error)", [#("error", string.inspect(e))])
          io.println_error("Failed to start TUI")
          halt(1)
        }
      }
      logger.info("After TUI block - should not reach here if TUI runs")
    }
    Error(e) -> {
      logger.error_ctx("Failed to start supervisor", [#("error", app_supervisor.error_to_string(e))])
      io.println_error("Failed to start application: " <> app_supervisor.error_to_string(e))
      halt(1)
    }
  }
}

@external(erlang, "erlang", "halt")
fn halt(code: Int) -> Nil
