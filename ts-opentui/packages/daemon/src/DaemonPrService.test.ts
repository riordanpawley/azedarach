import { describe, expect, it } from "bun:test"
import {
	AppConfig,
	AppConfigConfig,
	AppConfigProjectContext,
	type AppConfigProjectContextApi,
} from "@azedarach/config"
import type { TrackedIssue } from "@azedarach/shared/rpc"
import { BunContext } from "@effect/platform-bun"
import { Effect, Layer, Stream } from "effect"
import { DaemonAttachmentService } from "./DaemonAttachmentService.js"
import { DaemonPrService } from "./DaemonPrService.js"
import { DaemonSessionService, type DaemonSessionServiceApi } from "./DaemonSessionService.js"
import {
	TrackerIssueDaemonService,
	type TrackerIssueDaemonServiceApi,
} from "./TrackerIssueDaemonService.js"

const runCommand = (cwd: string, args: ReadonlyArray<string>): string => {
	const result = Bun.spawnSync({
		cmd: [...args],
		cwd,
		stdout: "pipe",
		stderr: "pipe",
	})
	if (result.exitCode !== 0) {
		throw new Error(
			`Command failed (${args.join(" ")}): ${Buffer.from(result.stderr).toString("utf8")}`,
		)
	}
	return Buffer.from(result.stdout).toString("utf8").trim()
}

const makeIssue = (issueId: string): TrackedIssue => ({
	id: issueId,
	title: `Issue ${issueId}`,
	status: "open",
	priority: 2,
	issue_type: "task",
	created_at: "2026-03-20T00:00:00.000Z",
	updated_at: "2026-03-20T00:00:00.000Z",
	closed_at: null,
	assignee: null,
	description: "Implement the change.",
	design: "Keep the daemon boundary intact.",
	acceptance: undefined,
	notes: undefined,
	estimate: undefined,
	labels: undefined,
	implementations: ["ts-opentui"],
	dependencies: undefined,
	dependents: undefined,
	dependency_count: undefined,
	dependent_count: undefined,
})

const makeTrackerStub = (issues: Map<string, TrackedIssue>): TrackerIssueDaemonServiceApi => ({
	get: (issueId) =>
		Effect.sync(() => {
			const issue = issues.get(issueId)
			if (issue === undefined) {
				throw new Error(`unknown issue ${issueId}`)
			}
			return issue
		}),
	list: () => Effect.dieMessage("Unexpected list"),
	create: () => Effect.dieMessage("Unexpected create"),
	update: (issueId, patch) =>
		Effect.sync(() => {
			const issue = issues.get(issueId)
			if (issue === undefined) {
				throw new Error(`unknown issue ${issueId}`)
			}
			issues.set(issueId, {
				...issue,
				notes: patch.notes ?? issue.notes,
				status: patch.status ?? issue.status,
			})
		}),
	addDependency: () => Effect.dieMessage("Unexpected addDependency"),
	removeDependency: () => Effect.dieMessage("Unexpected removeDependency"),
	close: (issueId) =>
		Effect.sync(() => {
			const issue = issues.get(issueId)
			if (issue === undefined) {
				throw new Error(`unknown issue ${issueId}`)
			}
			issues.set(issueId, {
				...issue,
				status: "closed",
			})
		}),
	delete: () => Effect.dieMessage("Unexpected delete"),
	sync: () => Effect.succeed({ pushed: 0, pulled: 0 }),
})

const makeSessionStub = (): DaemonSessionServiceApi => ({
	start: () => Effect.dieMessage("Unexpected start"),
	stop: (request) =>
		Effect.succeed({
			issueId: request.issueId,
			projectPath: request.projectPath,
			tmuxSessionName: `az-${request.issueId}`,
			worktreePath: null,
			state: "idle",
			startedAt: null,
		}),
	pause: () => Effect.dieMessage("Unexpected pause"),
	resume: () => Effect.dieMessage("Unexpected resume"),
	recover: () => Effect.dieMessage("Unexpected recover"),
})

const makeAttachmentStub = () =>
	DaemonAttachmentService.make({
		list: () => Effect.succeed([]),
		countBatch: () => Effect.succeed({}),
		attachFile: () => Effect.dieMessage("Unexpected attachFile"),
		attachClipboard: () => Effect.dieMessage("Unexpected attachClipboard"),
		remove: () => Effect.succeed(undefined),
		materializePath: () => Effect.dieMessage("Unexpected materializePath"),
	})

const makeLayer = (projectPath: string, issues: Map<string, TrackedIssue>) => {
	const projectContext: AppConfigProjectContextApi = {
		getCurrentPath: () => Effect.succeed(projectPath),
		currentProjectPathChanges: Stream.succeed(projectPath),
	}

	return Layer.provide(
		DaemonPrService.DefaultWithoutDependencies,
		Layer.mergeAll(
			BunContext.layer,
			Layer.succeed(
				AppConfigConfig,
				AppConfigConfig.make({
					projectPath,
					configPath: null,
				}),
			),
			Layer.succeed(AppConfigProjectContext, projectContext),
			AppConfig.Default,
			Layer.succeed(
				TrackerIssueDaemonService,
				TrackerIssueDaemonService.make(makeTrackerStub(issues)),
			),
			Layer.succeed(DaemonSessionService, DaemonSessionService.make(makeSessionStub())),
			Layer.succeed(DaemonAttachmentService, makeAttachmentStub()),
		),
	)
}

const setupRepo = async (issueId: string) => {
	const projectPath = `/tmp/azedarach-daemon-pr-${crypto.randomUUID()}`
	const worktreePath = `${projectPath}-${issueId}`
	const binDir = `${projectPath}/test-bin`
	const remotePath = `/tmp/azedarach-daemon-pr-remote-${crypto.randomUUID()}`

	await Bun.$`mkdir -p ${projectPath} ${binDir}`
	await Bun.write(`${projectPath}/README.md`, "base\n")
	runCommand(projectPath, ["git", "init", "-b", "main"])
	runCommand(projectPath, ["git", "config", "user.email", "test@example.com"])
	runCommand(projectPath, ["git", "config", "user.name", "Test User"])
	runCommand(projectPath, ["git", "add", "README.md"])
	runCommand(projectPath, ["git", "commit", "-m", "initial"])
	runCommand(projectPath, ["git", "init", "--bare", remotePath])
	runCommand(projectPath, ["git", "remote", "add", "origin", remotePath])
	runCommand(projectPath, ["git", "push", "-u", "origin", "main"])
	runCommand(projectPath, [
		"git",
		"worktree",
		"add",
		"-b",
		"test-user/AZ-1/issue",
		worktreePath,
		"main",
	])
	await Bun.write(`${worktreePath}/feature.txt`, "feature\n")

	await Bun.write(
		`${binDir}/gh`,
		[
			"#!/bin/sh",
			'if [ "$1" = "auth" ] && [ "$2" = "status" ]; then',
			"  exit 0",
			"fi",
			'if [ "$1" = "pr" ] && [ "$2" = "create" ]; then',
			'  printf "%s\\n" "https://example.com/org/repo/pull/42"',
			"  exit 0",
			"fi",
			'if [ "$1" = "pr" ] && [ "$2" = "view" ]; then',
			'  printf "%s\\n" \'{"number":42,"url":"https://example.com/org/repo/pull/42","title":"[task] Issue AZ-1 (AZ-1)","state":"OPEN","isDraft":true,"headRefName":"test-user/AZ-1/issue"}\'',
			"  exit 0",
			"fi",
			"exit 1",
			"",
		].join("\n"),
	)
	runCommand(projectPath, ["chmod", "+x", `${binDir}/gh`])

	return { projectPath, worktreePath, binDir, remotePath }
}

describe("DaemonPrService", () => {
	it("creates a PR via daemon-owned git/gh workflow and records the PR url on the issue", async () => {
		const issueId = "AZ-1"
		const issues = new Map<string, TrackedIssue>([[issueId, makeIssue(issueId)]])
		const { projectPath, worktreePath, binDir, remotePath } = await setupRepo(issueId)
		const originalPath = process.env.PATH ?? ""
		process.env.PATH = `${binDir}:${originalPath}`

		try {
			const pullRequest = await Effect.runPromise(
				Effect.gen(function* () {
					const service = yield* DaemonPrService
					return yield* service.create(issueId, projectPath)
				}).pipe(Effect.provide(makeLayer(projectPath, issues)), Effect.provide(BunContext.layer)),
			)

			expect(pullRequest.url).toBe("https://example.com/org/repo/pull/42")
			expect(pullRequest.branch).toBe("test-user/AZ-1/issue")
			expect(issues.get(issueId)?.notes).toContain("PR: https://example.com/org/repo/pull/42")
			expect(runCommand(worktreePath, ["git", "log", "-1", "--pretty=%s"])).toBe(
				"Complete AZ-1: Issue AZ-1",
			)
		} finally {
			process.env.PATH = originalPath
			await Bun.$`rm -rf ${projectPath} ${worktreePath} ${remotePath}`
		}
	})

	it("merges the worktree branch into main and cleans up the worktree when requested", async () => {
		const issueId = "AZ-1"
		const issues = new Map<string, TrackedIssue>([[issueId, makeIssue(issueId)]])
		const { projectPath, worktreePath, binDir, remotePath } = await setupRepo(issueId)
		const originalPath = process.env.PATH ?? ""
		process.env.PATH = `${binDir}:${originalPath}`

		try {
			await Effect.runPromise(
				Effect.gen(function* () {
					const service = yield* DaemonPrService
					yield* service.mergeToMain({ issueId, projectPath })
					yield* service.cleanup({ issueId, projectPath, closeIssue: true })
				}).pipe(Effect.provide(makeLayer(projectPath, issues)), Effect.provide(BunContext.layer)),
			)

			expect(runCommand(projectPath, ["git", "log", "-1", "--pretty=%s"])).toBe(
				"Merge AZ-1: Issue AZ-1",
			)
			expect(issues.get(issueId)?.status).toBe("closed")
			expect(await Bun.file(worktreePath).exists()).toBe(false)
		} finally {
			process.env.PATH = originalPath
			await Bun.$`rm -rf ${projectPath} ${worktreePath} ${remotePath}`
		}
	})
})
