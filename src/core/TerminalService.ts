import { Effect, Context, Layer, Data } from "effect"

export class NotInsideTmuxError extends Data.TaggedError("NotInsideTmuxError")<{
  message: string
}> {}

export class TmuxCommandError extends Data.TaggedError("TmuxCommandError")<{
  message: string
  command: string
}> {}

export interface TerminalServiceI {
  /**
   * Check if running inside a tmux session
   */
  readonly isInsideTmux: () => Effect.Effect<boolean, never>

  /**
   * Open a command in a new tmux window
   * Requires azedarach to be running inside tmux
   */
  readonly openInTmuxWindow: (cmd: string, windowName?: string) => Effect.Effect<void, NotInsideTmuxError | TmuxCommandError>

  /**
   * Switch to another tmux session
   * Use this to attach to Claude sessions - switches the whole client
   */
  readonly switchToSession: (sessionName: string) => Effect.Effect<void, NotInsideTmuxError | TmuxCommandError>
}

export class TerminalService extends Context.Tag("TerminalService")<TerminalService, TerminalServiceI>() {}

// Check if running inside tmux
const isInsideTmux = (): boolean => {
  return !!process.env.TMUX
}

export const TerminalServiceLive = Layer.succeed(TerminalService, {
  isInsideTmux: () =>
    Effect.sync(() => isInsideTmux()),

  openInTmuxWindow: (cmd: string, windowName?: string) =>
    Effect.gen(function* () {
      if (!isInsideTmux()) {
        return yield* Effect.fail(new NotInsideTmuxError({
          message: "Azedarach must be run inside tmux. Start with: tmux new-session -s az 'pnpm dev'"
        }))
      }

      const args = windowName
        ? ["tmux", "new-window", "-n", windowName, cmd]
        : ["tmux", "new-window", cmd]

      yield* Effect.tryPromise({
        try: () => Bun.spawn(args).exited,
        catch: (e) => new TmuxCommandError({
          message: `Failed to create tmux window: ${e}`,
          command: args.join(" ")
        })
      })
    }).pipe(Effect.asVoid),

  /**
   * Switch to another tmux session (for attaching to Claude sessions)
   */
  switchToSession: (sessionName: string) =>
    Effect.gen(function* () {
      if (!isInsideTmux()) {
        return yield* Effect.fail(new NotInsideTmuxError({
          message: "Azedarach must be run inside tmux. Start with: tmux new-session -s az 'pnpm dev'"
        }))
      }

      yield* Effect.tryPromise({
        try: () => Bun.spawn(["tmux", "switch-client", "-t", sessionName]).exited,
        catch: (e) => new TmuxCommandError({
          message: `Failed to switch to session: ${e}`,
          command: `tmux switch-client -t ${sessionName}`
        })
      })
    }).pipe(Effect.asVoid)
})
