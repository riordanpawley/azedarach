import { describe, expect, it } from "bun:test"
import type { SessionStateUpdate } from "../core/TmuxSessionMonitor.js"
import type { Project } from "../services/ProjectService.js"
import {
	deriveCurrentProjectWaitingIssueIds,
	deriveWaitingSessionOptions,
} from "./waitingSessions.js"

const makeSession = (
	overrides: Partial<SessionStateUpdate> & Pick<SessionStateUpdate, "issueId" | "sessionName">,
): SessionStateUpdate => ({
	issueId: overrides.issueId,
	status: overrides.status ?? "waiting",
	sessionName: overrides.sessionName,
	createdAt: overrides.createdAt ?? 0,
	worktreePath: overrides.worktreePath ?? null,
	projectPath: overrides.projectPath ?? null,
})

describe("deriveWaitingSessionOptions", () => {
	it("keeps current-project waiting sessions first and uses registered project names", () => {
		const projects: readonly Project[] = [
			{ name: "alpha", path: "/tmp/alpha" },
			{ name: "beta", path: "/tmp/beta" },
		]

		const sessions: readonly SessionStateUpdate[] = [
			makeSession({
				issueId: "ir",
				sessionName: "codex-alpha-ir",
				projectPath: "/tmp/alpha",
			}),
			makeSession({
				issueId: "ja",
				sessionName: "codex-beta-ja",
				projectPath: "/tmp/beta",
			}),
			makeSession({
				issueId: "done",
				sessionName: "codex-beta-done",
				projectPath: "/tmp/beta",
				status: "busy",
			}),
		]

		expect(deriveWaitingSessionOptions(sessions, projects, "/tmp/beta")).toEqual([
			{
				issueId: "ja",
				sessionName: "codex-beta-ja",
				projectPath: "/tmp/beta",
				projectName: "beta",
				isCurrentProject: true,
				isRegisteredProject: true,
			},
			{
				issueId: "ir",
				sessionName: "codex-alpha-ir",
				projectPath: "/tmp/alpha",
				projectName: "alpha",
				isCurrentProject: false,
				isRegisteredProject: true,
			},
		])
	})

	it("falls back to path-derived names and sorts unregistered projects after registered ones", () => {
		const projects: readonly Project[] = [{ name: "alpha", path: "/tmp/alpha" }]

		const sessions: readonly SessionStateUpdate[] = [
			makeSession({
				issueId: "zz",
				sessionName: "codex-external-zz",
				projectPath: "/tmp/external",
			}),
			makeSession({
				issueId: "aa",
				sessionName: "codex-alpha-aa",
				projectPath: "/tmp/alpha",
			}),
			makeSession({
				issueId: "unknown",
				sessionName: "codex-unknown",
				projectPath: null,
			}),
		]

		expect(deriveWaitingSessionOptions(sessions, projects, undefined)).toEqual([
			{
				issueId: "aa",
				sessionName: "codex-alpha-aa",
				projectPath: "/tmp/alpha",
				projectName: "alpha",
				isCurrentProject: false,
				isRegisteredProject: true,
			},
			{
				issueId: "zz",
				sessionName: "codex-external-zz",
				projectPath: "/tmp/external",
				projectName: "external",
				isCurrentProject: false,
				isRegisteredProject: false,
			},
			{
				issueId: "unknown",
				sessionName: "codex-unknown",
				projectPath: null,
				projectName: "Unknown project",
				isCurrentProject: false,
				isRegisteredProject: false,
			},
		])
	})
})

describe("deriveCurrentProjectWaitingIssueIds", () => {
	it("keeps current-project issue IDs in waiting-session order and removes duplicates", () => {
		expect(
			deriveCurrentProjectWaitingIssueIds([
				{
					issueId: "ia",
					sessionName: "az-ia",
					projectPath: "/tmp/alpha",
					projectName: "alpha",
					isCurrentProject: true,
					isRegisteredProject: true,
				},
				{
					issueId: "ia",
					sessionName: "az-ia-shadow",
					projectPath: "/tmp/alpha",
					projectName: "alpha",
					isCurrentProject: true,
					isRegisteredProject: true,
				},
				{
					issueId: "jj",
					sessionName: "az-jj",
					projectPath: "/tmp/alpha",
					projectName: "alpha",
					isCurrentProject: true,
					isRegisteredProject: true,
				},
				{
					issueId: "zz",
					sessionName: "az-zz",
					projectPath: "/tmp/other",
					projectName: "other",
					isCurrentProject: false,
					isRegisteredProject: true,
				},
			]),
		).toEqual(["ia", "jj"])
	})

	it("excludes waiting sessions from other projects", () => {
		expect(
			deriveCurrentProjectWaitingIssueIds([
				{
					issueId: "zz",
					sessionName: "az-zz",
					projectPath: "/tmp/other",
					projectName: "other",
					isCurrentProject: false,
					isRegisteredProject: true,
				},
			]),
		).toEqual([])
	})
})
