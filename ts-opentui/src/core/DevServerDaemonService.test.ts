import { describe, expect, it } from "bun:test"
import { Effect } from "effect"
import { makeDevServerDaemonService } from "./DevServerDaemonService.js"

describe("DevServerDaemonService", () => {
	it("returns deterministic idle status for unknown servers", async () => {
		const service = await Effect.runPromise(
			makeDevServerDaemonService({
				nowMs: () => 1_000,
				portBase: 4_400,
			}),
		)

		const status = await Effect.runPromise(
			service.status({
				issueId: "qp",
				projectPath: "/tmp/project-a",
			}),
		)

		expect(status.capturedAtMs).toBe(1_000)
		expect(status.server.issueId).toBe("qp")
		expect(status.server.serverName).toBe("default")
		expect(status.server.status).toBe("idle")
		expect(status.server.projectPath).toBe("/tmp/project-a")
		expect(status.server.port).toBeNull()
	})

	it("allocates ports deterministically and keeps start idempotent per server key", async () => {
		const nowValues = [2_000, 2_010, 2_020, 2_030]
		const service = await Effect.runPromise(
			makeDevServerDaemonService({
				nowMs: () => nowValues.shift() ?? 2_030,
				portBase: 5_000,
			}),
		)

		const startedDefault = await Effect.runPromise(
			service.start({
				issueId: "qp",
				projectPath: "/tmp/project-a",
			}),
		)
		const startedApi = await Effect.runPromise(
			service.start({
				issueId: "qp",
				projectPath: "/tmp/project-a",
				serverName: "api",
			}),
		)
		const startedAgain = await Effect.runPromise(
			service.start({
				issueId: "qp",
				projectPath: "/tmp/project-a",
			}),
		)
		const listed = await Effect.runPromise(service.list({ issueId: "qp" }))

		expect(startedDefault.server.status).toBe("running")
		expect(startedDefault.server.port).toBe(5_000)
		expect(startedDefault.server.startedAt).toBe("1970-01-01T00:00:02.000Z")
		expect(startedApi.server.port).toBe(5_001)
		expect(startedAgain.server.port).toBe(5_000)
		expect(listed.servers.map((server) => server.serverName)).toEqual(["api", "default"])
		expect(listed.servers[0]?.port).toBe(5_001)
		expect(listed.servers[1]?.port).toBe(5_000)
	})

	it("stops servers deterministically and keeps state visible in status/list", async () => {
		const nowValues = [3_000, 3_100, 3_200, 3_300]
		const service = await Effect.runPromise(
			makeDevServerDaemonService({
				nowMs: () => nowValues.shift() ?? 3_300,
				portBase: 6_000,
			}),
		)

		await Effect.runPromise(
			service.start({
				issueId: "qr",
				projectPath: "/tmp/project-b",
				serverName: "web",
			}),
		)
		const stopped = await Effect.runPromise(
			service.stop({
				issueId: "qr",
				serverName: "web",
			}),
		)
		const status = await Effect.runPromise(
			service.status({
				issueId: "qr",
				serverName: "web",
			}),
		)
		const listed = await Effect.runPromise(service.list({ projectPath: "/tmp/project-b" }))

		expect(stopped.server.status).toBe("stopped")
		expect(stopped.server.projectPath).toBe("/tmp/project-b")
		expect(stopped.server.port).toBeNull()
		expect(status.server.status).toBe("stopped")
		expect(listed.servers).toHaveLength(1)
		expect(listed.servers[0]?.serverName).toBe("web")
		expect(listed.servers[0]?.status).toBe("stopped")
	})
})
