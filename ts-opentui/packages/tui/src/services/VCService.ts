import { Command } from "@effect/platform"
import * as PlatformCommandExecutor from "@effect/platform/CommandExecutor"
import { BunContext } from "@effect/platform-bun"
import { Data, Effect, Ref, Schedule } from "effect"
import { DiagnosticsService } from "./DiagnosticsService.js"
import { type TmuxError, TmuxService } from "./TmuxService.js"

const VC_SESSION_NAME = "vc-autopilot"
const STATUS_POLL_INTERVAL = "5 seconds"

export type VCStatus = "not_installed" | "stopped" | "starting" | "running" | "error"

export interface VCExecutorInfo {
	readonly status: VCStatus
	readonly sessionName: string
	readonly pid?: number
	readonly startedAt?: Date
	readonly lastActivity?: Date
}

export class VCNotInstalledError extends Data.TaggedError("VCNotInstalledError")<{
	readonly message: string
}> {}

export class VCError extends Data.TaggedError("VCError")<{
	readonly message: string
	readonly command?: string
	readonly stderr?: string
}> {}

export class VCNotRunningError extends Data.TaggedError("VCNotRunningError")<{
	readonly message: string
}> {}

export interface VCServiceImpl {
	readonly isAvailable: () => Effect.Effect<boolean, never>
	readonly getVersion: () => Effect.Effect<string, VCNotInstalledError>
	readonly startAutoPilot: () => Effect.Effect<
		VCExecutorInfo,
		VCNotInstalledError | VCError | TmuxError
	>
	readonly stopAutoPilot: () => Effect.Effect<void, VCError | TmuxError>
	readonly isAutoPilotRunning: () => Effect.Effect<boolean, never>
	readonly getStatus: () => Effect.Effect<VCExecutorInfo, never>
	readonly sendCommand: (command: string) => Effect.Effect<void, VCNotRunningError | TmuxError>
	readonly getAttachCommand: () => Effect.Effect<string, VCNotRunningError>
	readonly toggleAutoPilot: () => Effect.Effect<
		VCExecutorInfo,
		VCNotInstalledError | VCError | TmuxError
	>
}

const notInstalledError = () =>
	new VCNotInstalledError({
		message: "VC is not installed. Install with: brew tap steveyegge/vc && brew install vc",
	})

export class VCService extends Effect.Service<VCService>()("VCService", {
	dependencies: [BunContext.layer, TmuxService.Default, DiagnosticsService.Default],
	scoped: Effect.gen(function* () {
		const tmux = yield* TmuxService
		const diagnostics = yield* DiagnosticsService
		const commandExecutor = yield* PlatformCommandExecutor.CommandExecutor
		yield* diagnostics.trackService("VCService", "5s VC status polling")

		const executorStateRef = yield* Ref.make<VCExecutorInfo>({
			status: "stopped",
			sessionName: VC_SESSION_NAME,
		})

		const checkVCInstalled = (): Effect.Effect<boolean, never> =>
			commandExecutor.exitCode(Command.make("which", "vc")).pipe(
				Effect.map((exitCode) => exitCode === 0),
				Effect.catchAll((error) =>
					Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
						Effect.zipRight(Effect.succeed(false)),
					),
				),
			)

		const updateState = (
			update: Partial<VCExecutorInfo>,
		): Effect.Effect<VCExecutorInfo, never, never> =>
			Ref.updateAndGet(executorStateRef, (current) => ({ ...current, ...update }))

		yield* Effect.scheduleForked(Schedule.spaced(STATUS_POLL_INTERVAL))(
			Effect.log("todo: poll vc status"),
		)

		return {
			isAvailable: () => checkVCInstalled(),
			getVersion: () =>
				Effect.gen(function* () {
					const installed = yield* checkVCInstalled()
					if (!installed) {
						return yield* Effect.fail(notInstalledError())
					}
					return yield* commandExecutor.string(Command.make("vc", "--version")).pipe(
						Effect.map((version) => version.trim()),
						Effect.mapError(
							(error) =>
								new VCNotInstalledError({ message: `Failed to get VC version: ${String(error)}` }),
						),
					)
				}),
			startAutoPilot: () =>
				Effect.gen(function* () {
					const installed = yield* checkVCInstalled()
					if (!installed) {
						return yield* Effect.fail(notInstalledError())
					}

					const hasSession = yield* tmux.hasSession(VC_SESSION_NAME)
					if (hasSession) {
						return yield* updateState({
							status: "running",
							lastActivity: new Date(),
						})
					}

					yield* updateState({ status: "starting" })
					yield* tmux.newSession(VC_SESSION_NAME, { command: "vc" })
					yield* Effect.sleep("1 second")

					const running = yield* tmux.hasSession(VC_SESSION_NAME)
					if (!running) {
						yield* updateState({ status: "error" })
						return yield* Effect.fail(new VCError({ message: "Failed to start VC session" }))
					}
					return yield* updateState({
						status: "running",
						startedAt: new Date(),
						lastActivity: new Date(),
					})
				}),
			stopAutoPilot: () =>
				Effect.gen(function* () {
					const hasSession = yield* tmux.hasSession(VC_SESSION_NAME)
					if (!hasSession) {
						yield* updateState({ status: "stopped" })
						return
					}

					yield* tmux
						.sendKeys(VC_SESSION_NAME, "/exit")
						.pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.void),
								),
							),
						)
					yield* Effect.sleep("500 millis")

					const stillRunning = yield* tmux.hasSession(VC_SESSION_NAME)
					if (stillRunning) {
						yield* tmux
							.killSession(VC_SESSION_NAME)
							.pipe(
								Effect.catchAll((error) =>
									Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
										Effect.zipRight(Effect.void),
									),
								),
							)
					}

					yield* updateState({
						status: "stopped",
						startedAt: undefined,
						pid: undefined,
					})
				}),
			isAutoPilotRunning: () =>
				Effect.gen(function* () {
					const hasSession = yield* tmux
						.hasSession(VC_SESSION_NAME)
						.pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.succeed(false)),
								),
							),
						)
					if (hasSession) {
						yield* updateState({ status: "running", lastActivity: new Date() })
					} else {
						const current = yield* Ref.get(executorStateRef)
						if (current.status === "running") {
							yield* updateState({ status: "stopped" })
						}
					}
					return hasSession
				}),
			getStatus: () =>
				Effect.gen(function* () {
					const hasSession = yield* tmux
						.hasSession(VC_SESSION_NAME)
						.pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.succeed(false)),
								),
							),
						)
					const installed = yield* checkVCInstalled()
					if (!installed) {
						return yield* updateState({ status: "not_installed" })
					}
					if (hasSession) {
						return yield* updateState({ status: "running", lastActivity: new Date() })
					}
					const current = yield* Ref.get(executorStateRef)
					if (current.status === "running" || current.status === "starting") {
						return yield* updateState({ status: "stopped" })
					}
					return current
				}),
			sendCommand: (command: string) =>
				Effect.gen(function* () {
					const hasSession = yield* tmux
						.hasSession(VC_SESSION_NAME)
						.pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.succeed(false)),
								),
							),
						)
					if (!hasSession) {
						return yield* Effect.fail(
							new VCNotRunningError({
								message: "VC auto-pilot is not running. Start it first with toggleAutoPilot()",
							}),
						)
					}
					yield* tmux
						.sendKeys(VC_SESSION_NAME, command)
						.pipe(
							Effect.catchTag("SessionNotFoundError", () =>
								Effect.fail(new VCNotRunningError({ message: "VC session not found" })),
							),
						)
					yield* updateState({ lastActivity: new Date() })
				}),
			getAttachCommand: () =>
				Effect.gen(function* () {
					const hasSession = yield* tmux
						.hasSession(VC_SESSION_NAME)
						.pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.succeed(false)),
								),
							),
						)
					if (!hasSession) {
						return yield* Effect.fail(
							new VCNotRunningError({
								message: "VC auto-pilot is not running",
							}),
						)
					}
					return `tmux attach -t ${VC_SESSION_NAME}`
				}),
			toggleAutoPilot: () =>
				Effect.gen(function* () {
					const hasSession = yield* tmux
						.hasSession(VC_SESSION_NAME)
						.pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.succeed(false)),
								),
							),
						)
					if (hasSession) {
						yield* tmux
							.killSession(VC_SESSION_NAME)
							.pipe(
								Effect.catchAll((error) =>
									Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
										Effect.zipRight(Effect.void),
									),
								),
							)
						return yield* updateState({ status: "stopped" })
					}

					const installed = yield* checkVCInstalled()
					if (!installed) {
						return yield* Effect.fail(notInstalledError())
					}

					yield* updateState({ status: "starting" })
					yield* tmux.newSession(VC_SESSION_NAME, { command: "vc" })
					yield* Effect.sleep("1 second")

					const running = yield* tmux
						.hasSession(VC_SESSION_NAME)
						.pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.succeed(false)),
								),
							),
						)

					if (running) {
						return yield* updateState({
							status: "running",
							startedAt: new Date(),
							lastActivity: new Date(),
						})
					}
					return yield* updateState({ status: "error" })
				}),
		} satisfies VCServiceImpl
	}),
}) {}

export const VCServiceLive = VCService.Default
