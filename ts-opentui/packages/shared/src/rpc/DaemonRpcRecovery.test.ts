import { describe, expect, it } from "bun:test"
import { Effect } from "effect"
import {
	daemonRpcMethodUnavailableError,
	invokeOptionalDaemonRpcMethod,
	mapDaemonRpcClientErrorMessage,
} from "./DaemonRpcRecovery.js"

describe("DaemonRpcRecovery", () => {
	it("creates a typed unavailable-method error", () => {
		const error = daemonRpcMethodUnavailableError("prCheckGhCli")

		expect(error._tag).toBe("DaemonRpcMethodUnavailableError")
		expect(error.methodName).toBe("prCheckGhCli")
		expect(error.message).toBe("Daemon RPC method is unavailable: prCheckGhCli")
	})

	it("maps daemon rpc client errors through the provided constructor", () => {
		const error = mapDaemonRpcClientErrorMessage(
			{
				_tag: "DaemonRpcActionError",
				action: "attachmentList",
				code: "test-failure",
				message: "attachment lookup failed",
			},
			(message) => ({ message }),
		)

		expect(error).toEqual({ message: "attachment lookup failed" })
	})

	it("recovers missing optional daemon rpc methods as typed failures", async () => {
		const exit = await Effect.runPromiseExit(
			invokeOptionalDaemonRpcMethod({
				method: undefined,
				methodName: "prCheckGhCli",
				request: undefined,
				onUnavailable: daemonRpcMethodUnavailableError,
				onError: (error) => new Error(error.message),
			}),
		)

		expect(exit._tag).toBe("Failure")
		if (exit._tag === "Failure") {
			expect(exit.cause._tag).toBe("Fail")
			if (exit.cause._tag === "Fail") {
				expect(exit.cause.error._tag).toBe("DaemonRpcMethodUnavailableError")
			}
		}
	})
})
