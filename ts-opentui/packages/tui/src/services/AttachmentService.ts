import { Data, Effect } from "effect"
import { TerminalService } from "./TerminalService.js"
import { TmuxService } from "./TmuxService.js"

export class AttachmentError extends Data.TaggedError("AttachmentError")<{
	readonly message: string
	readonly sessionId?: string
	readonly causeMessage?: string
}> {}

export class SessionNotFoundError extends Data.TaggedError("SessionNotFoundError")<{
	readonly sessionId: string
}> {}

export class TerminalError extends Data.TaggedError("TerminalError")<{
	readonly message: string
	readonly terminalType?: string
}> {}

export type AttachmentMode = "external" | "inline"

export interface AttachmentEvent {
	readonly sessionId: string
	readonly mode: AttachmentMode
	readonly timestamp: Date
}

export interface AttachmentServiceI {
	readonly attachExternal: (
		sessionId: string,
	) => Effect.Effect<void, AttachmentError | SessionNotFoundError>
	readonly attachInline: (
		sessionId: string,
	) => Effect.Effect<void, AttachmentError | SessionNotFoundError>
	readonly getAttachmentHistory: () => Effect.Effect<ReadonlyArray<AttachmentEvent>, never>
	readonly hasAttached: (sessionId: string) => Effect.Effect<boolean, never>
}

export class AttachmentService extends Effect.Service<AttachmentService>()("AttachmentService", {
	dependencies: [TmuxService.Default, TerminalService.Default],
	effect: Effect.gen(function* () {
		const tmux = yield* TmuxService
		const _terminal = yield* TerminalService
		const attachmentHistory: Array<AttachmentEvent> = []

		const recordAttachment = (sessionId: string, mode: AttachmentMode): void => {
			attachmentHistory.push({
				sessionId,
				mode,
				timestamp: new Date(),
			})
		}

		const ensureSessionExists = (
			sessionId: string,
		): Effect.Effect<void, AttachmentError | SessionNotFoundError> =>
			tmux.hasSession(sessionId).pipe(
				Effect.mapError(
					(error) =>
						new AttachmentError({
							message: `Failed to check session existence: ${String(error)}`,
							sessionId,
							causeMessage: String(error),
						}),
				),
				Effect.flatMap((exists) =>
					exists ? Effect.void : Effect.fail(new SessionNotFoundError({ sessionId })),
				),
			)

		return {
			attachExternal: (sessionId: string) =>
				Effect.gen(function* () {
					yield* ensureSessionExists(sessionId)
					yield* tmux.switchClient(sessionId).pipe(
						Effect.mapError(
							(error) =>
								new AttachmentError({
									message: `Failed to switch to session: ${String(error)}`,
									sessionId,
									causeMessage: String(error),
								}),
						),
					)
					yield* Effect.sync(() => recordAttachment(sessionId, "external"))
				}),
			attachInline: (sessionId: string) =>
				Effect.gen(function* () {
					yield* ensureSessionExists(sessionId)
					return yield* Effect.fail(
						new AttachmentError({
							message: "Inline attachment not implemented yet. Use external attachment instead.",
							sessionId,
						}),
					)
				}),
			getAttachmentHistory: () => Effect.succeed([...attachmentHistory]),
			hasAttached: (sessionId: string) =>
				Effect.succeed(attachmentHistory.some((event) => event.sessionId === sessionId)),
		} satisfies AttachmentServiceI
	}),
}) {}

export const AttachmentServiceLive = AttachmentService.Default
