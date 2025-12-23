/**
 * SessionMigration - Migrate legacy tmux sessions to unified format
 *
 * Migrates from prefix-based session naming (claude-{beadId}, opencode-{beadId}, dev-{beadId})
 * to unified format ({beadId} with windows: code, dev, chat, background).
 *
 * Chat sessions (chat-{beadId}) remain separate by design.
 */

import { Data, Effect, Option } from "effect"
import { isAiToolSession, parseSessionName, WINDOW_NAMES } from "./paths.js"
import { TmuxService } from "./TmuxService.js"

export interface LegacySession {
	readonly sessionName: string
	readonly beadId: string
	readonly type: "claude" | "opencode" | "dev"
}

export interface MigrationPlan {
	readonly beadId: string
	readonly unifiedSessionName: string
	readonly legacySessions: readonly LegacySession[]
	readonly hasExistingUnified: boolean
}

export interface MigrationResult {
	readonly migratedBeads: readonly string[]
	readonly errors: readonly { beadId: string; error: string }[]
}

export class SessionMigrationError extends Data.TaggedError("SessionMigrationError")<{
	readonly message: string
	readonly beadId?: string
}> {}

export class SessionMigration extends Effect.Service<SessionMigration>()("SessionMigration", {
	dependencies: [TmuxService.Default],
	effect: Effect.gen(function* () {
		const tmux = yield* TmuxService

		const findLegacySessions = Effect.gen(function* () {
			const sessions = yield* tmux.listSessions()
			const legacy: LegacySession[] = []

			for (const session of sessions) {
				const parsed = parseSessionName(session.name)
				if (!parsed) continue
				if (parsed.type === "chat") continue
				if (parsed.type === "bead") continue

				if (isAiToolSession(parsed.type) || parsed.type === "dev") {
					legacy.push({
						sessionName: session.name,
						beadId: parsed.beadId,
						type: parsed.type,
					})
				}
			}

			return legacy
		})

		const createMigrationPlans = (legacySessions: readonly LegacySession[]) =>
			Effect.gen(function* () {
				const byBeadId = new Map<string, LegacySession[]>()
				for (const session of legacySessions) {
					const existing = byBeadId.get(session.beadId) ?? []
					byBeadId.set(session.beadId, [...existing, session])
				}

				const plans: MigrationPlan[] = []
				for (const [beadId, sessions] of byBeadId.entries()) {
					const unifiedSessionName = beadId
					const hasExistingUnified = yield* tmux.hasSession(unifiedSessionName)

					plans.push({
						beadId,
						unifiedSessionName,
						legacySessions: sessions,
						hasExistingUnified,
					})
				}

				return plans
			})

		const getTargetWindowName = (type: "claude" | "opencode" | "dev"): string => {
			if (type === "claude" || type === "opencode") return WINDOW_NAMES.CODE
			if (type === "dev") return WINDOW_NAMES.DEV
			return "unknown"
		}

		const migrateBead = (plan: MigrationPlan) =>
			Effect.gen(function* () {
				yield* Effect.log(
					`Migrating ${plan.beadId}: ${plan.legacySessions.length} legacy sessions → unified`,
				)

				const { beadId, unifiedSessionName, legacySessions, hasExistingUnified } = plan

				let unifiedExists = hasExistingUnified
				const sorted = [...legacySessions].sort((a, b) => {
					if (isAiToolSession(a.type) && !isAiToolSession(b.type)) return -1
					if (!isAiToolSession(a.type) && isAiToolSession(b.type)) return 1
					return 0
				})

				for (let i = 0; i < sorted.length; i++) {
					const legacy = sorted[i]
					const targetWindow = getTargetWindowName(legacy.type)

					if (!unifiedExists && i === 0) {
						yield* Effect.log(`  Renaming ${legacy.sessionName} → ${unifiedSessionName}`)
						yield* tmux.renameSession(legacy.sessionName, unifiedSessionName).pipe(
							Effect.mapError(
								(e) =>
									new SessionMigrationError({
										message: `Failed to rename session: ${e}`,
										beadId,
									}),
							),
						)

						const windows = yield* tmux.listWindows(unifiedSessionName)
						if (windows.length > 0) {
							yield* tmux.renameWindow(unifiedSessionName, "0", targetWindow).pipe(Effect.ignore)
						}

						unifiedExists = true
					} else {
						yield* Effect.log(
							`  Moving window from ${legacy.sessionName} → ${unifiedSessionName}:${targetWindow}`,
						)

						const targetExists = yield* tmux.hasWindow(unifiedSessionName, targetWindow)

						if (!targetExists) {
							yield* tmux
								.linkWindow(`${legacy.sessionName}:0`, `${unifiedSessionName}:${targetWindow}`)
								.pipe(
									Effect.catchAll((e) =>
										Effect.logWarning(`Failed to link window: ${e}`).pipe(Effect.as(0)),
									),
								)
						} else {
							yield* Effect.logWarning(
								`  Window ${targetWindow} already exists in ${unifiedSessionName}, skipping`,
							)
						}

						yield* tmux.killSession(legacy.sessionName).pipe(Effect.catchAll(() => Effect.void))
					}
				}

				for (const legacy of sorted) {
					const worktree = yield* tmux
						.getUserOption(legacy.sessionName, "@az_worktree")
						.pipe(Effect.catchAll(() => Effect.succeed(Option.none())))
					const project = yield* tmux
						.getUserOption(legacy.sessionName, "@az_project")
						.pipe(Effect.catchAll(() => Effect.succeed(Option.none())))

					if (Option.isSome(worktree)) {
						yield* tmux
							.setUserOption(unifiedSessionName, "@az_worktree", worktree.value)
							.pipe(Effect.ignore)
					}
					if (Option.isSome(project)) {
						yield* tmux
							.setUserOption(unifiedSessionName, "@az_project", project.value)
							.pipe(Effect.ignore)
					}
				}

				yield* Effect.log(`✓ Migrated ${beadId}`)
			})

		const needsMigration = Effect.gen(function* () {
			const legacy = yield* findLegacySessions
			return legacy.length > 0
		})

		const migrate = Effect.gen(function* () {
			const legacy = yield* findLegacySessions

			if (legacy.length === 0) {
				yield* Effect.log("No legacy sessions found - migration not needed")
				return {
					migratedBeads: [],
					errors: [],
				}
			}

			yield* Effect.log(`Found ${legacy.length} legacy sessions to migrate`)

			const plans = yield* createMigrationPlans(legacy)
			const results: string[] = []
			const errors: { beadId: string; error: string }[] = []

			for (const plan of plans) {
				const result = yield* migrateBead(plan).pipe(
					Effect.map(() => plan.beadId),
					Effect.catchAll((e) =>
						Effect.gen(function* () {
							yield* Effect.logError(`Failed to migrate ${plan.beadId}: ${e}`)
							errors.push({
								beadId: plan.beadId,
								error: e instanceof SessionMigrationError ? e.message : String(e),
							})
							return null
						}),
					),
				)

				if (result) results.push(result)
			}

			yield* Effect.log(
				`Migration complete: ${results.length} beads migrated, ${errors.length} errors`,
			)

			return {
				migratedBeads: results,
				errors,
			}
		})

		return {
			needsMigration,
			migrate,
			findLegacySessions,
		}
	}),
}) {}
