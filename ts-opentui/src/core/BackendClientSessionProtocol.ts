import { Data, Effect } from "effect"

export type BackendClientProtocolOperation = "attach" | "reconnect"

export type BackendClientProtocolCompatibilityDecision =
	| "exact-match"
	| "client-older-compatible"
	| "server-older-compatible"
	| "incompatible"

export interface BackendClientSessionIdentity {
	readonly clientId: string
}

export interface BackendClientSessionNegotiatedCapabilities {
	readonly authoritativeRuntime: true
	readonly lifecycleGenerationTracking: true
	readonly recoveryGenerationTracking: true
	readonly resumeToken: true
}

export interface BackendClientProtocolHandshakeMetadata {
	readonly operation: BackendClientProtocolOperation
	readonly requestedAtMs: number
	readonly negotiatedAtMs: number
	readonly requestedProtocolVersion: number
	readonly negotiatedProtocolVersion: number
	readonly serverSupportedProtocolVersions: ReadonlyArray<number>
	readonly compatibilityDecision: Exclude<
		BackendClientProtocolCompatibilityDecision,
		"incompatible"
	>
}

export interface BackendClientAttachIntent {
	readonly operation: "attach"
	readonly identity: BackendClientSessionIdentity
	readonly requestedAtMs: number
	readonly requestedProtocolVersion: number
}

export interface BackendClientReconnectIntent {
	readonly operation: "reconnect"
	readonly identity: BackendClientSessionIdentity
	readonly requestedAtMs: number
	readonly requestedProtocolVersion: number
	readonly lastSeenRevision: number | null
	readonly lastSeenLifecycleGeneration: number | null
}

export type BackendClientSessionIntent = BackendClientAttachIntent | BackendClientReconnectIntent

export const BACKEND_CLIENT_SESSION_PROTOCOL_VERSION = 1

export const BACKEND_CLIENT_SESSION_SUPPORTED_PROTOCOL_VERSIONS: ReadonlyArray<number> = [
	BACKEND_CLIENT_SESSION_PROTOCOL_VERSION,
]

export const BACKEND_CLIENT_SESSION_NEGOTIATED_CAPABILITIES: BackendClientSessionNegotiatedCapabilities =
	{
		authoritativeRuntime: true,
		lifecycleGenerationTracking: true,
		recoveryGenerationTracking: true,
		resumeToken: true,
	}

export class BackendClientProtocolVersionMismatchError extends Data.TaggedError(
	"BackendDaemonProtocolVersionMismatchError",
)<{
	readonly operation: BackendClientProtocolOperation
	readonly compatibilityDecision: "incompatible"
	readonly serverSupportedProtocolVersions: ReadonlyArray<number>
	readonly expectedProtocolVersion: number
	readonly receivedProtocolVersion: number
}> {}

const normalizeRequestedProtocolVersion = (requestedProtocolVersion: number | undefined): number =>
	requestedProtocolVersion ?? BACKEND_CLIENT_SESSION_PROTOCOL_VERSION

export const createBackendClientAttachIntent = (params: {
	readonly clientId: string
	readonly requestedAtMs: number
	readonly requestedProtocolVersion?: number
}): BackendClientAttachIntent => ({
	operation: "attach",
	identity: { clientId: params.clientId },
	requestedAtMs: params.requestedAtMs,
	requestedProtocolVersion: normalizeRequestedProtocolVersion(params.requestedProtocolVersion),
})

export const createBackendClientReconnectIntent = (params: {
	readonly clientId: string
	readonly requestedAtMs: number
	readonly requestedProtocolVersion?: number
	readonly lastSeenRevision?: number
	readonly lastSeenLifecycleGeneration?: number
}): BackendClientReconnectIntent => ({
	operation: "reconnect",
	identity: { clientId: params.clientId },
	requestedAtMs: params.requestedAtMs,
	requestedProtocolVersion: normalizeRequestedProtocolVersion(params.requestedProtocolVersion),
	lastSeenRevision: params.lastSeenRevision ?? null,
	lastSeenLifecycleGeneration: params.lastSeenLifecycleGeneration ?? null,
})

export const negotiateBackendClientProtocolHandshake = (
	intent: BackendClientSessionIntent,
): Effect.Effect<BackendClientProtocolHandshakeMetadata> => {
	if (intent.requestedProtocolVersion !== BACKEND_CLIENT_SESSION_PROTOCOL_VERSION) {
		return Effect.die(
			new BackendClientProtocolVersionMismatchError({
				operation: intent.operation,
				compatibilityDecision: "incompatible",
				serverSupportedProtocolVersions: BACKEND_CLIENT_SESSION_SUPPORTED_PROTOCOL_VERSIONS,
				expectedProtocolVersion: BACKEND_CLIENT_SESSION_PROTOCOL_VERSION,
				receivedProtocolVersion: intent.requestedProtocolVersion,
			}),
		)
	}

	return Effect.succeed({
		operation: intent.operation,
		requestedAtMs: intent.requestedAtMs,
		negotiatedAtMs: intent.requestedAtMs,
		requestedProtocolVersion: intent.requestedProtocolVersion,
		negotiatedProtocolVersion: BACKEND_CLIENT_SESSION_PROTOCOL_VERSION,
		serverSupportedProtocolVersions: BACKEND_CLIENT_SESSION_SUPPORTED_PROTOCOL_VERSIONS,
		compatibilityDecision: "exact-match",
	})
}

export const createBackendClientResumeToken = (
	identity: BackendClientSessionIdentity,
	revision: number,
): string => `${identity.clientId}:${String(revision)}`
