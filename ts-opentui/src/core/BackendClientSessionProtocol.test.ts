import { describe, expect, it } from "bun:test"
import {
	BACKEND_CLIENT_SESSION_PROTOCOL_VERSION,
	BACKEND_SYSTEM_AUTH_CONTEXT,
	createBackendClientAttachIntent,
	createBackendClientProtocolAuditEvent,
	createBackendClientReconnectIntent,
	createBackendClientResumeToken,
	hasBackendClientCapability,
	negotiateBackendClientProtocolHandshake,
	requireBackendClientCapability,
} from "@azedarach/shared/rpc"
import { Cause, Effect, Exit, Option } from "effect"

describe("BackendClientSessionProtocol", () => {
	it("normalizes attach/reconnect intents with explicit identity, auth, and reconnect cursor", () => {
		const attachIntent = createBackendClientAttachIntent({
			clientId: "client-a",
			requestedAtMs: 1_000,
		})
		const reconnectIntent = createBackendClientReconnectIntent({
			clientId: "client-a",
			requestedAtMs: 1_100,
			lastSeenRevision: 2,
			lastSeenLifecycleGeneration: 1,
		})

		expect(attachIntent.identity).toEqual({ clientId: "client-a" })
		expect(attachIntent.auth.actorId).toBe("local-client")
		expect(attachIntent.auth.capabilities).toEqual([
			"session:attach",
			"session:heartbeat",
			"session:reconnect",
		])
		expect(reconnectIntent.lastSeenRevision).toBe(2)
		expect(reconnectIntent.lastSeenLifecycleGeneration).toBe(1)
	})

	it("negotiates exact-match metadata and deterministic resume tokens", async () => {
		const intent = createBackendClientAttachIntent({
			clientId: "client-a",
			requestedAtMs: 3_000,
			requestedProtocolVersion: BACKEND_CLIENT_SESSION_PROTOCOL_VERSION,
		})
		const handshake = await Effect.runPromise(negotiateBackendClientProtocolHandshake(intent))

		expect(handshake).toMatchObject({
			operation: "attach",
			requestedAtMs: 3_000,
			negotiatedAtMs: 3_000,
			requestedProtocolVersion: BACKEND_CLIENT_SESSION_PROTOCOL_VERSION,
			negotiatedProtocolVersion: BACKEND_CLIENT_SESSION_PROTOCOL_VERSION,
			compatibilityDecision: "exact-match",
		})
		expect(createBackendClientResumeToken(intent.identity, 42)).toBe("client-a:42")
	})

	it("enforces capability checks for privileged operations", async () => {
		const deniedExit = await Effect.runPromiseExit(
			requireBackendClientCapability({
				auth: createBackendClientAttachIntent({
					clientId: "client-a",
					requestedAtMs: 4_000,
				}).auth,
				capability: "runtime:restart",
				operation: "runtime.restart",
			}),
		)
		expect(Exit.isFailure(deniedExit)).toBe(true)
		if (!Exit.isFailure(deniedExit)) {
			throw new Error("Expected capability denial")
		}
		const deniedDefect = Cause.dieOption(deniedExit.cause)
		expect(Option.isSome(deniedDefect)).toBe(true)
		if (!Option.isSome(deniedDefect)) {
			throw new Error("Expected capability denial defect")
		}
		expect(deniedDefect.value).toMatchObject({
			_tag: "BackendDaemonAuthorizationError",
			operation: "runtime.restart",
			requiredCapability: "runtime:restart",
		})

		expect(hasBackendClientCapability(BACKEND_SYSTEM_AUTH_CONTEXT, "runtime:restart")).toBe(true)
		await Effect.runPromise(
			requireBackendClientCapability({
				auth: BACKEND_SYSTEM_AUTH_CONTEXT,
				capability: "runtime:restart",
				operation: "runtime.restart",
			}),
		)
	})

	it("creates typed audit events for allow/deny outcomes", () => {
		const allowed = createBackendClientProtocolAuditEvent({
			occurredAtMs: 5_000,
			operation: "runtime.restart",
			auth: BACKEND_SYSTEM_AUTH_CONTEXT,
			capability: "runtime:restart",
			outcome: "allowed",
			reason: "capability granted",
		})
		expect(allowed).toEqual({
			occurredAtMs: 5_000,
			operation: "runtime.restart",
			actorId: "daemon-system",
			trustLevel: "system",
			capability: "runtime:restart",
			outcome: "allowed",
			reason: "capability granted",
		})
	})

	it("dies with protocol mismatch error when versions are incompatible", async () => {
		const intent = createBackendClientReconnectIntent({
			clientId: "client-a",
			requestedAtMs: 6_000,
			requestedProtocolVersion: 99,
		})

		const exit = await Effect.runPromiseExit(negotiateBackendClientProtocolHandshake(intent))
		expect(Exit.isFailure(exit)).toBe(true)
		if (!Exit.isFailure(exit)) {
			throw new Error("Expected protocol negotiation failure")
		}
		const defect = Cause.dieOption(exit.cause)
		expect(Option.isSome(defect)).toBe(true)
		if (!Option.isSome(defect)) {
			throw new Error("Expected mismatch defect")
		}
		expect(defect.value).toMatchObject({
			_tag: "BackendDaemonProtocolVersionMismatchError",
			operation: "reconnect",
			compatibilityDecision: "incompatible",
			expectedProtocolVersion: BACKEND_CLIENT_SESSION_PROTOCOL_VERSION,
			receivedProtocolVersion: 99,
		})
	})
})
