import { describe, expect, it } from "bun:test"
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { BunContext } from "@effect/platform-bun"
import { DateTime, Effect, HashMap, Layer } from "effect"
import { SessionStateStore } from "./SessionStateStore.js"
import type { SessionState } from "./StateDetector.js"

interface LegacySessionPayload {
	readonly issueId: string
	readonly worktreePath: string
	readonly tmuxSessionName: string
	readonly state: SessionState
	readonly startedAt: string
	readonly projectPath?: string
}

const loadSessions = (projectPath: string) =>
	Effect.runPromise(
		Effect.gen(function* () {
			const store = yield* SessionStateStore
			return yield* store.load(projectPath)
		}).pipe(Effect.provide(Layer.provide(SessionStateStore.Default, BunContext.layer))),
	)

const writeLegacySessionsFile = (
	projectPath: string,
	sessions: readonly LegacySessionPayload[],
) => {
	const storageDir = join(projectPath, ".azedarach")
	const legacyFilePath = join(storageDir, "sessions.json")
	mkdirSync(storageDir, { recursive: true })
	const encoded = sessions.map((session) => [session.issueId, session])
	writeFileSync(legacyFilePath, JSON.stringify(encoded), "utf8")
	return legacyFilePath
}

describe("SessionStateStore migration", () => {
	it("migrates legacy sessions.json to sqlite and removes the legacy file", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-session-store-"))
		const startedAtIso = "2026-03-06T12:00:00.000Z"

		try {
			const legacyFilePath = writeLegacySessionsFile(projectPath, [
				{
					issueId: "bm",
					worktreePath: join(projectPath, "worktrees", "bm"),
					tmuxSessionName: "az-bm",
					state: "busy",
					startedAt: startedAtIso,
					projectPath,
				},
			])

			const loaded = await loadSessions(projectPath)
			const sessions = Array.from(HashMap.values(loaded))

			expect(sessions).toHaveLength(1)
			expect(sessions[0]?.issueId).toBe("bm")
			expect(sessions[0]?.tmuxSessionName).toBe("az-bm")
			expect(sessions[0]?.state).toBe("busy")
			expect(DateTime.formatIso(sessions[0]!.startedAt)).toBe(startedAtIso)
			expect(existsSync(legacyFilePath)).toBe(false)
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})

	it("runs legacy migration only once per project", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-session-store-once-"))

		try {
			writeLegacySessionsFile(projectPath, [
				{
					issueId: "bm",
					worktreePath: join(projectPath, "worktrees", "bm"),
					tmuxSessionName: "az-bm",
					state: "waiting",
					startedAt: "2026-03-06T13:00:00.000Z",
					projectPath,
				},
			])

			const firstLoad = await loadSessions(projectPath)
			expect(Array.from(HashMap.keys(firstLoad))).toEqual(["bm"])

			writeLegacySessionsFile(projectPath, [
				{
					issueId: "stale",
					worktreePath: join(projectPath, "worktrees", "stale"),
					tmuxSessionName: "az-stale",
					state: "error",
					startedAt: "2026-03-06T14:00:00.000Z",
					projectPath,
				},
			])

			const secondLoad = await loadSessions(projectPath)
			expect(Array.from(HashMap.keys(secondLoad))).toEqual(["bm"])
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})

	it("filters migrated legacy sessions by project scope", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-session-store-scope-"))
		const otherProjectPath = join(projectPath, "other-project")

		try {
			writeLegacySessionsFile(projectPath, [
				{
					issueId: "bm",
					worktreePath: join(projectPath, "worktrees", "bm"),
					tmuxSessionName: "az-bm",
					state: "busy",
					startedAt: "2026-03-06T15:00:00.000Z",
					projectPath,
				},
				{
					issueId: "offscope",
					worktreePath: join(otherProjectPath, "worktrees", "offscope"),
					tmuxSessionName: "ot-offscope",
					state: "busy",
					startedAt: "2026-03-06T15:10:00.000Z",
					projectPath: otherProjectPath,
				},
			])

			const loaded = await loadSessions(projectPath)
			expect(Array.from(HashMap.keys(loaded))).toEqual(["bm"])
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})
})
