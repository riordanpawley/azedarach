import { describe, expect, it } from "bun:test"
import {
	DAEMON_RPC_PROTOCOL_VERSION,
	DaemonRpcClient,
	type DaemonRpcClientApi,
	type DaemonRpcClientError,
} from "@azedarach/shared/rpc"
import { Effect, Layer } from "effect"
import { getImplementationRegistryFromDaemon } from "./daemonRpc.js"

const unexpectedDaemonRpcCall = <A>(): Effect.Effect<A, DaemonRpcClientError> =>
	Effect.dieMessage("Unexpected daemon rpc call in daemonRpc test")

const makeDaemonRpcClientStub = (
	registry: DaemonRpcClientApi["implementationGetRegistry"],
): DaemonRpcClientApi => ({
	status: () => unexpectedDaemonRpcCall(),
	health: () => unexpectedDaemonRpcCall(),
	logs: () => unexpectedDaemonRpcCall(),
	stop: () => unexpectedDaemonRpcCall(),
	restart: () => unexpectedDaemonRpcCall(),
	attach: () => unexpectedDaemonRpcCall(),
	reconnect: () => unexpectedDaemonRpcCall(),
	heartbeat: () => unexpectedDaemonRpcCall(),
	eventStream: () => unexpectedDaemonRpcCall(),
	sessionSnapshot: () => unexpectedDaemonRpcCall(),
	boardReadModel: () => unexpectedDaemonRpcCall(),
	sessionStart: () => unexpectedDaemonRpcCall(),
	sessionStop: () => unexpectedDaemonRpcCall(),
	sessionPause: () => unexpectedDaemonRpcCall(),
	sessionResume: () => unexpectedDaemonRpcCall(),
	sessionRecover: () => unexpectedDaemonRpcCall(),
	sessionUpdateState: () => unexpectedDaemonRpcCall(),
	devServerStatus: () => unexpectedDaemonRpcCall(),
	devServerList: () => unexpectedDaemonRpcCall(),
	devServerStart: () => unexpectedDaemonRpcCall(),
	devServerStop: () => unexpectedDaemonRpcCall(),
	queueEnqueue: () => unexpectedDaemonRpcCall(),
	queueQuery: () => unexpectedDaemonRpcCall(),
	queueCancel: () => unexpectedDaemonRpcCall(),
	issueGet: () => unexpectedDaemonRpcCall(),
	issueList: () => unexpectedDaemonRpcCall(),
	issueCreate: () => unexpectedDaemonRpcCall(),
	issueUpdate: () => unexpectedDaemonRpcCall(),
	issueAddDependency: () => unexpectedDaemonRpcCall(),
	issueRemoveDependency: () => unexpectedDaemonRpcCall(),
	issueClose: () => unexpectedDaemonRpcCall(),
	issueDelete: () => unexpectedDaemonRpcCall(),
	issueSync: () => unexpectedDaemonRpcCall(),
	implementationGetRegistry: registry,
	implementationCreate: () => unexpectedDaemonRpcCall(),
	implementationUpdate: () => unexpectedDaemonRpcCall(),
	implementationDelete: () => unexpectedDaemonRpcCall(),
	implementationSetDefault: () => unexpectedDaemonRpcCall(),
	specRequirementList: () => unexpectedDaemonRpcCall(),
	specRequirementGet: () => unexpectedDaemonRpcCall(),
	specRequirementCreate: () => unexpectedDaemonRpcCall(),
	specRequirementUpdate: () => unexpectedDaemonRpcCall(),
	specRequirementDelete: () => unexpectedDaemonRpcCall(),
	specRead: () => unexpectedDaemonRpcCall(),
	specLint: () => unexpectedDaemonRpcCall(),
	specParity: () => unexpectedDaemonRpcCall(),
	specIssueLinks: () => unexpectedDaemonRpcCall(),
	specRequirementIssues: () => unexpectedDaemonRpcCall(),
	specLinkList: () => unexpectedDaemonRpcCall(),
	specLinkAdd: () => unexpectedDaemonRpcCall(),
	specLinkRemove: () => unexpectedDaemonRpcCall(),
	specLinkUpdate: () => unexpectedDaemonRpcCall(),
	specPublishConfigGet: () => unexpectedDaemonRpcCall(),
	specPublishConfigSet: () => unexpectedDaemonRpcCall(),
	specPublishOutcomeGet: () => unexpectedDaemonRpcCall(),
	specSyncMarkdown: () => unexpectedDaemonRpcCall(),
	specPublish: () => unexpectedDaemonRpcCall(),
})

describe("getImplementationRegistryFromDaemon", () => {
	it("reads the implementation registry from daemon rpc", async () => {
		const registry = {
			default_implementation: "default",
			implicit_default_allowed: false,
			implementations: [
				{
					name: "default",
					description: undefined,
					directory: undefined,
					created_at: "2026-01-01T00:00:00.000Z",
					updated_at: "2026-01-01T00:00:00.000Z",
					is_default: true,
					is_builtin: true,
				},
				{
					name: "alt",
					description: "Alternative",
					directory: "/tmp/alt",
					created_at: "2026-01-02T00:00:00.000Z",
					updated_at: "2026-01-02T00:00:00.000Z",
					is_default: false,
					is_builtin: false,
				},
			],
		}

		const effect = getImplementationRegistryFromDaemon("/tmp/project")
		const result = await Effect.runPromise(
			effect.pipe(
				Effect.provide(
					Layer.succeed(
						DaemonRpcClient,
						makeDaemonRpcClientStub((_request) =>
							Effect.succeed({
								rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
								registry,
							}),
						),
					),
				),
			),
		)

		expect(result).toEqual(registry)
	})
})
