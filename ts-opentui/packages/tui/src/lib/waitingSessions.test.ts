import { describe, expect, it } from "bun:test"
import {
	deriveCurrentProjectWaitingIssueIds,
	deriveWaitingSessionOptions,
	type WaitingSessionSource,
} from "./waitingSessions.js"

const session = (
	overrides: Partial<WaitingSessionSource> & Pick<WaitingSessionSource, "issueId" | "sessionName">,
): WaitingSessionSource => ({
	issueId: overrides.issueId,
	sessionName: overrides.sessionName,
	projectPath: overrides.projectPath ?? null,
	status: overrides.status ?? "waiting",
})

describe("waitingSessions", () => {
	it("orders current project sessions first and deduplicates issue ids", () => {
		const sessions: readonly WaitingSessionSource[] = [
			session({ issueId: "az-2", sessionName: "two", projectPath: "/work/beta" }),
			session({ issueId: "az-1", sessionName: "one", projectPath: "/work/alpha" }),
			session({ issueId: "az-1", sessionName: "one-b", projectPath: "/work/alpha" }),
			session({
				issueId: "az-3",
				sessionName: "three",
				projectPath: "/work/gamma",
				status: "idle",
			}),
		]
		const projects = [
			{ name: "alpha", path: "/work/alpha" },
			{ name: "beta", path: "/work/beta" },
		]

		const waitingSessions = deriveWaitingSessionOptions(sessions, projects, "/work/alpha")
		expect(waitingSessions.map((value) => value.issueId)).toEqual(["az-1", "az-1", "az-2"])
		expect(deriveCurrentProjectWaitingIssueIds(waitingSessions)).toEqual(["az-1"])
	})
})
