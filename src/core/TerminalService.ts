import { Effect, Context, Layer, Data } from "effect"

export class TerminalNotFoundError extends Data.TaggedError("TerminalNotFoundError")<{
  message?: string
}> {}

export type TerminalType = "ghostty" | "iterm" | "terminal-app" | "unknown"

export interface TerminalServiceI {
  readonly detect: () => Effect.Effect<TerminalType, never>
  readonly openWithCommand: (cmd: string) => Effect.Effect<void, TerminalNotFoundError>
}

export class TerminalService extends Context.Tag("TerminalService")<TerminalService, TerminalServiceI>() {}

// Helper function to detect terminal type
const detectTerminal = (): TerminalType => {
  const term = process.env.TERM_PROGRAM?.toLowerCase()
  if (term?.includes("ghostty")) return "ghostty"
  if (term?.includes("iterm")) return "iterm"
  if (term?.includes("apple_terminal")) return "terminal-app"
  return "unknown"
}

export const TerminalServiceLive = Layer.succeed(TerminalService, {
  detect: () =>
    Effect.sync(() => detectTerminal()),

  openWithCommand: (cmd: string) =>
    Effect.gen(function* () {
      const terminal = detectTerminal()

      switch (terminal) {
        case "ghostty":
          yield* Effect.tryPromise(() =>
            Bun.spawn(["ghostty", "-e", cmd]).exited
          )
          break
        case "iterm":
          // Use osascript for iTerm
          yield* Effect.tryPromise(() =>
            Bun.spawn(["osascript", "-e", `tell application "iTerm" to create window with default profile command "${cmd}"`]).exited
          )
          break
        default:
          // Fallback to open with Terminal.app
          yield* Effect.tryPromise(() =>
            Bun.spawn(["open", "-a", "Terminal", cmd]).exited
          )
      }
    }).pipe(
      Effect.asVoid,
      Effect.catchAll(() => Effect.fail(new TerminalNotFoundError({})))
    )
})
