import { describe, expect, it } from "bun:test"
import { Cause, Effect, Exit, Option } from "effect"
import {
	BACKEND_CLIENT_SESSION_PROTOCOL_VERSION,
	createBackendClientAttachIntent,
	createBackendClientReconnectIntent,
	createBackendClientResumeToken,
	negotiateBackendClientProtocolHandshake,
} from "./BackendClientSessionProtocol.js"

describe("BackendClientSessionProtocol", () => {
	it("normalizes attach/reconnect intents with explicit session identity", () => {
		const attachIntent = createBackendClientAttachIntent({
			clientId: "client-a",
			requestedAtMs: 1_000,
		})
		const reconnectIntent = createBackendClientReconnectIntent({
			clientId: "client-a",
			requestedAtMs: 1_100,
		})

		expect(attachIntent).toEqual({
			operation: "attach",
			identity: { clientId: "client-a" },
			requestedAtMs: 1_000,
			requestedProtocolVersion: BACKEND_CLIENT_SESSION_PROTOCOL_VERSION,
		})
		expect(reconnectIntent).toEqual({
			operation: "reconnect",
			identity: { clientId: "client-a" },
			requestedAtMs: 1_100,
			requestedProtocolVersion: BACKEND_CLIENT_SESSION_PROTOCOL_VERSION,
			lastSeenRevision: null,
			lastSeenLifecycleGeneration: null,
		})
	})

	it("preserves reconnect cursor metadata when provided", () => {
		const reconnectIntent = createBackendClientReconnectIntent({
			clientId: "client-a",
			requestedAtMs: 2_000,
			requestedProtocolVersion: BACKEND_CLIENT_SESSION_PROTOCOL_VERSION,
			lastSeenRevision: 7,
			lastSeenLifecycleGeneration: 3,
		})

		expect(reconnectIntent.lastSeenRevision).toBe(7)
		expect(reconnectIntent.lastSeenLifecycleGeneration).toBe(3)
	})

	it("negotiates exact-match protocol metadata and deterministic resume tokens", async () => {
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

	it("dies with protocol mismatch error when versions are incompatible", async () => {
		const intent = createBackendClientReconnectIntent({
			clientId: "client-a",
			requestedAtMs: 4_000,
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
