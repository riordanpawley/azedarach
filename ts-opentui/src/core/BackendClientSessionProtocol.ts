import { Data, Effect } from "effect"

export type BackendClientProtocolOperation = "attach" | "reconnect"

export type BackendClientProtocolCompatibilityDecision =
	| "exact-match"
	| "client-older-compatible"
	| "server-older-compatible"
	| "incompatible"

export type BackendClientTrustLevel = "system" | "trusted-local" | "untrusted-local"

export type BackendClientCapability =
	| "session:attach"
	| "session:reconnect"
	| "session:heartbeat"
	| "runtime:restart"

export interface BackendClientSessionIdentity {
	readonly clientId: string
}

export interface BackendClientAuthContext {
	readonly actorId: string
	readonly trustLevel: BackendClientTrustLevel
	readonly capabilities: ReadonlyArray<BackendClientCapability>
}

export interface BackendClientSessionNegotiatedCapabilities {
	readonly authoritativeRuntime: true
	readonly lifecycleGenerationTracking: true
	readonly recoveryGenerationTracking: true
	readonly resumeToken: true
	readonly clientCapabilities: ReadonlyArray<BackendClientCapability>
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
	readonly auth: BackendClientAuthContext
	readonly requestedAtMs: number
	readonly requestedProtocolVersion: number
}

export interface BackendClientReconnectIntent {
	readonly operation: "reconnect"
	readonly identity: BackendClientSessionIdentity
	readonly auth: BackendClientAuthContext
	readonly requestedAtMs: number
	readonly requestedProtocolVersion: number
	readonly lastSeenRevision: number | null
	readonly lastSeenLifecycleGeneration: number | null
}

export type BackendClientSessionIntent = BackendClientAttachIntent | BackendClientReconnectIntent

export type BackendClientProtocolAuditOperation =
	| "client.attach"
	| "client.reconnect"
	| "client.heartbeat"
	| "runtime.restart"

export interface BackendClientProtocolAuditEvent {
	readonly occurredAtMs: number
	readonly operation: BackendClientProtocolAuditOperation
	readonly actorId: string
	readonly trustLevel: BackendClientTrustLevel
	readonly capability: BackendClientCapability | null
	readonly outcome: "allowed" | "denied"
	readonly reason: string
}

export const BACKEND_CLIENT_SESSION_PROTOCOL_VERSION = 1

export const BACKEND_CLIENT_SESSION_SUPPORTED_PROTOCOL_VERSIONS: ReadonlyArray<number> = [
	BACKEND_CLIENT_SESSION_PROTOCOL_VERSION,
]

export const BACKEND_DEFAULT_CLIENT_CAPABILITIES: ReadonlyArray<BackendClientCapability> = [
	"session:attach",
	"session:heartbeat",
	"session:reconnect",
]

export const BACKEND_SYSTEM_CLIENT_CAPABILITIES: ReadonlyArray<BackendClientCapability> = [
	...BACKEND_DEFAULT_CLIENT_CAPABILITIES,
	"runtime:restart",
]

const sortCapabilities = (
	capabilities: ReadonlyArray<BackendClientCapability>,
): ReadonlyArray<BackendClientCapability> =>
	[...capabilities].sort((left, right) => left.localeCompare(right))

const dedupeCapabilities = (
	capabilities: ReadonlyArray<BackendClientCapability>,
): ReadonlyArray<BackendClientCapability> => Array.from(new Set(capabilities))

export const BACKEND_DEFAULT_CLIENT_AUTH_CONTEXT: BackendClientAuthContext = {
	actorId: "local-client",
	trustLevel: "trusted-local",
	capabilities: sortCapabilities(BACKEND_DEFAULT_CLIENT_CAPABILITIES),
}

export const BACKEND_SYSTEM_AUTH_CONTEXT: BackendClientAuthContext = {
	actorId: "daemon-system",
	trustLevel: "system",
	capabilities: sortCapabilities(BACKEND_SYSTEM_CLIENT_CAPABILITIES),
}

export const BACKEND_CLIENT_SESSION_NEGOTIATED_CAPABILITIES: BackendClientSessionNegotiatedCapabilities =
	{
		authoritativeRuntime: true,
		lifecycleGenerationTracking: true,
		recoveryGenerationTracking: true,
		resumeToken: true,
		clientCapabilities: BACKEND_DEFAULT_CLIENT_AUTH_CONTEXT.capabilities,
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

export class BackendClientCapabilityDeniedError extends Data.TaggedError(
	"BackendDaemonAuthorizationError",
)<{
	readonly operation: BackendClientProtocolAuditOperation
	readonly actorId: string
	readonly trustLevel: BackendClientTrustLevel
	readonly requiredCapability: BackendClientCapability
}> {}

const normalizeRequestedProtocolVersion = (requestedProtocolVersion: number | undefined): number =>
	requestedProtocolVersion ?? BACKEND_CLIENT_SESSION_PROTOCOL_VERSION

const normalizeAuthContext = (
	auth: BackendClientAuthContext | undefined,
	fallback: BackendClientAuthContext,
): BackendClientAuthContext => {
	if (auth === undefined) {
		return fallback
	}
	const capabilities = sortCapabilities(dedupeCapabilities(auth.capabilities))
	return {
		actorId: auth.actorId,
		trustLevel: auth.trustLevel,
		capabilities,
	}
}

export const createBackendClientAttachIntent = (params: {
	readonly clientId: string
	readonly requestedAtMs: number
	readonly requestedProtocolVersion?: number
	readonly auth?: BackendClientAuthContext
}): BackendClientAttachIntent => ({
	operation: "attach",
	identity: { clientId: params.clientId },
	auth: normalizeAuthContext(params.auth, BACKEND_DEFAULT_CLIENT_AUTH_CONTEXT),
	requestedAtMs: params.requestedAtMs,
	requestedProtocolVersion: normalizeRequestedProtocolVersion(params.requestedProtocolVersion),
})

export const createBackendClientReconnectIntent = (params: {
	readonly clientId: string
	readonly requestedAtMs: number
	readonly requestedProtocolVersion?: number
	readonly auth?: BackendClientAuthContext
	readonly lastSeenRevision?: number
	readonly lastSeenLifecycleGeneration?: number
}): BackendClientReconnectIntent => ({
	operation: "reconnect",
	identity: { clientId: params.clientId },
	auth: normalizeAuthContext(params.auth, BACKEND_DEFAULT_CLIENT_AUTH_CONTEXT),
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

export const hasBackendClientCapability = (
	auth: BackendClientAuthContext,
	capability: BackendClientCapability,
): boolean => auth.capabilities.includes(capability)

export const requireBackendClientCapability = (params: {
	readonly auth: BackendClientAuthContext
	readonly capability: BackendClientCapability
	readonly operation: BackendClientProtocolAuditOperation
}): Effect.Effect<void> =>
	hasBackendClientCapability(params.auth, params.capability)
		? Effect.void
		: Effect.die(
				new BackendClientCapabilityDeniedError({
					operation: params.operation,
					actorId: params.auth.actorId,
					trustLevel: params.auth.trustLevel,
					requiredCapability: params.capability,
				}),
			)

export const createBackendClientProtocolAuditEvent = (params: {
	readonly occurredAtMs: number
	readonly operation: BackendClientProtocolAuditOperation
	readonly auth: BackendClientAuthContext
	readonly capability: BackendClientCapability | null
	readonly outcome: "allowed" | "denied"
	readonly reason: string
}): BackendClientProtocolAuditEvent => ({
	occurredAtMs: params.occurredAtMs,
	operation: params.operation,
	actorId: params.auth.actorId,
	trustLevel: params.auth.trustLevel,
	capability: params.capability,
	outcome: params.outcome,
	reason: params.reason,
})
