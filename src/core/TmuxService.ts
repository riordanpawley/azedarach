import { Effect, Context, Layer, Data } from "effect"
import { Command, CommandExecutor } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"

// Errors
export class TmuxNotFoundError extends Data.TaggedError("TmuxNotFoundError")<{}> {}
export class SessionNotFoundError extends Data.TaggedError("SessionNotFoundError")<{ session: string }> {}
export class TmuxError extends Data.TaggedError("TmuxError")<{ message: string }> {}

// Session info type
export interface TmuxSession {
  name: string
  windows: number
  created: Date
  attached: boolean
}

// Service interface
export interface TmuxServiceI {
  readonly newSession: (name: string, opts?: {
    cwd?: string
    command?: string
  }) => Effect.Effect<void, TmuxError>

  readonly killSession: (name: string) => Effect.Effect<void, TmuxError | SessionNotFoundError>

  readonly listSessions: () => Effect.Effect<TmuxSession[], TmuxError>

  readonly hasSession: (name: string) => Effect.Effect<boolean, TmuxError>

  readonly sendKeys: (session: string, keys: string) => Effect.Effect<void, TmuxError | SessionNotFoundError>

  readonly attachCommand: (session: string) => string  // Returns the command string to attach
}

// Service Tag
export class TmuxService extends Context.Tag("TmuxService")<TmuxService, TmuxServiceI>() {}

// Helper to run tmux commands
const runTmux = (args: string[]): Effect.Effect<string, TmuxError, CommandExecutor.CommandExecutor> =>
  Effect.gen(function* () {
    const command = Command.make("tmux", ...args)
    return yield* Command.string(command)
  }).pipe(
    Effect.mapError((e) => new TmuxError({ message: String(e) }))
  )

// Service implementation
const TmuxServiceImpl = Effect.gen(function* () {
  return {
    newSession: (name: string, opts?: { cwd?: string; command?: string }) =>
      Effect.gen(function* () {
        const args = ["new-session", "-d", "-s", name]
        if (opts?.cwd) args.push("-c", opts.cwd)
        if (opts?.command) args.push(opts.command)
        yield* runTmux(args)
      }),

    killSession: (name: string) =>
      runTmux(["kill-session", "-t", name]).pipe(
        Effect.asVoid,
        Effect.catchAll(() => Effect.fail(new SessionNotFoundError({ session: name })))
      ),

    listSessions: () =>
      Effect.gen(function* () {
        const output = yield* runTmux(["list-sessions", "-F", "#{session_name}:#{session_windows}:#{session_created}:#{session_attached}"])
        return output.trim().split("\n").filter(Boolean).map(line => {
          const [name, windows, created, attached] = line.split(":")
          return {
            name,
            windows: parseInt(windows, 10),
            created: new Date(parseInt(created, 10) * 1000),
            attached: attached === "1"
          }
        })
      }).pipe(
        Effect.catchAll(() => Effect.succeed([]))  // No sessions = empty list
      ),

    hasSession: (name: string) =>
      Effect.gen(function* () {
        const service = yield* TmuxService
        const sessions = yield* service.listSessions()
        return sessions.some((s: TmuxSession) => s.name === name)
      }),

    sendKeys: (session: string, keys: string) =>
      runTmux(["send-keys", "-t", session, keys, "Enter"]).pipe(
        Effect.asVoid,
        Effect.catchAll(() => Effect.fail(new SessionNotFoundError({ session })))
      ),

    attachCommand: (session: string) => `tmux attach-session -t ${session}`
  }
})

// Live implementation with BunContext
export const TmuxServiceLive = Layer.effect(
  TmuxService,
  TmuxServiceImpl as unknown as Effect.Effect<TmuxServiceI, never, never>
).pipe(Layer.provide(BunContext.layer))
