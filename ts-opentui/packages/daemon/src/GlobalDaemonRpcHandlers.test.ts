import { describe, expect, it } from "bun:test"
import {
	DAEMON_RPC_PROTOCOL_VERSION,
	type DaemonBoardTask,
	type ImageAttachment,
} from "@azedarach/shared/rpc"
import { Effect } from "effect"
import type { BackendDaemonSessionSnapshot } from "./BackendDaemonSessionRecovery.js"
import { DaemonAttachmentError } from "./DaemonAttachmentService.js"
import { DaemonPrError } from "./DaemonPrService.js"
import { DaemonSessionError } from "./DaemonSessionService.js"
import {
	makeDaemonAttachmentAttachClipboardRpcHandler,
	makeDaemonAttachmentAttachFileRpcHandler,
	makeDaemonAttachmentCountBatchRpcHandler,
	makeDaemonAttachmentListRpcHandler,
	makeDaemonAttachmentMaterializePathRpcHandler,
	makeDaemonAttachmentRemoveRpcHandler,
	makeDaemonBoardReadModelRpcHandler,
	makeDaemonPrAbortMergeRpcHandler,
	makeDaemonPrCheckGhCliRpcHandler,
	makeDaemonPrCleanupRpcHandler,
	makeDaemonPrCreateRpcHandler,
	makeDaemonPrMergeBaseIntoBranchRpcHandler,
	makeDaemonPrMergeToMainRpcHandler,
	makeDaemonPrUpdateFromBaseRpcHandler,
	makeDaemonSessionPauseRpcHandler,
	makeDaemonSessionRecoverRpcHandler,
	makeDaemonSessionResumeRpcHandler,
	makeDaemonSessionSnapshotRpcHandler,
	makeDaemonSessionStartRpcHandler,
	makeDaemonSessionStopRpcHandler,
	makeDaemonSessionUpdateStateRpcHandler,
} from "./GlobalDaemonRpcHandlers.js"

const task: DaemonBoardTask = {
	id: "task-1",
	title: "Board task",
	status: "in_progress",
	priority: 2,
	issue_type: "task",
	created_at: "2026-03-20T00:00:00.000Z",
	updated_at: "2026-03-20T01:00:00.000Z",
	implementations: ["ts-opentui"],
	sessionState: "busy",
}

const sessionSnapshot: BackendDaemonSessionSnapshot = {
	issueId: "task-1",
	state: "busy",
	projectPath: "/tmp/project",
	tmuxSessionName: "az-task-1",
	worktreePath: "/tmp/project/.worktrees/task-1",
	startedAt: "2026-03-20T02:00:00.000Z",
}

const imageAttachment: ImageAttachment = {
	id: "att-1",
	filename: "att-1.png",
	originalPath: "/tmp/source.png",
	mimeType: "image/png",
	size: 42,
	createdAt: "2026-03-20T02:00:00.000Z",
}

describe("makeDaemonBoardReadModelRpcHandler", () => {
	it("maps daemon control board snapshots into the RPC envelope", async () => {
		const handler = makeDaemonBoardReadModelRpcHandler({
			boardReadModel: (request) => {
				expect(request.projectPath).toBe("/tmp/project")
				return Effect.succeed({
					capturedAtMs: 123,
					projectPath: "/tmp/project",
					tasks: [task],
				})
			},
		})

		const result = await Effect.runPromise(
			handler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
			}),
		)

		expect(result).toEqual({
			rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			capturedAtMs: 123,
			projectPath: "/tmp/project",
			tasks: [task],
		})
	})

	it("maps board read model failures into daemon rpc action errors", async () => {
		const handler = makeDaemonBoardReadModelRpcHandler({
			boardReadModel: () => Effect.fail(new Error("board read failed")),
		})

		const exit = await Effect.runPromiseExit(
			handler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
			}),
		)

		expect(exit._tag).toBe("Failure")
		if (exit._tag === "Failure" && exit.cause._tag === "Fail") {
			expect(exit.cause.error).toEqual({
				_tag: "DaemonRpcActionError",
				action: "boardReadModel",
				code: "daemon-operation-failed",
				message: "board read failed",
			})
		}
	})
})

describe("makeDaemonSessionSnapshotRpcHandler", () => {
	it("maps daemon session telemetry into the RPC envelope", async () => {
		const handler = makeDaemonSessionSnapshotRpcHandler({
			listActive: (projectPath) => {
				expect(projectPath).toBe("/tmp/project")
				return Effect.succeed([
					{
						issueId: "task-1",
						state: "waiting",
						projectPath,
						tmuxSessionName: "az-task-1",
						worktreePath: "/tmp/project/.worktrees/task-1",
						startedAt: "2026-03-20T02:00:00.000Z",
					},
				])
			},
		})

		const result = await Effect.runPromise(
			handler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
			}),
		)

		expect(result.sessions[0]).toEqual({
			issueId: "task-1",
			state: "waiting",
			projectPath: "/tmp/project",
			tmuxSessionName: "az-task-1",
			worktreePath: "/tmp/project/.worktrees/task-1",
			startedAt: "2026-03-20T02:00:00.000Z",
		})
	})
})

describe("session mutation rpc handlers", () => {
	it("maps session start results into the RPC envelope", async () => {
		const handler = makeDaemonSessionStartRpcHandler({
			start: (request) => {
				expect(request).toEqual({
					issueId: "task-1",
					projectPath: "/tmp/project",
					initialPrompt: "Start the work",
					imagePaths: ["/tmp/image-1.png"],
					sessionEnv: {
						FOO: "bar",
					},
					dangerouslySkipPermissions: true,
				})
				return Effect.succeed(sessionSnapshot)
			},
		})

		const result = await Effect.runPromise(
			handler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				projectPath: "/tmp/project",
				initialPrompt: "Start the work",
				imagePaths: ["/tmp/image-1.png"],
				sessionEnv: {
					FOO: "bar",
				},
				dangerouslySkipPermissions: true,
			}),
		)

		expect(result.rpcProtocolVersion).toBe(DAEMON_RPC_PROTOCOL_VERSION)
		expect(result.session).toEqual(sessionSnapshot)
		expect(typeof result.capturedAtMs).toBe("number")
	})

	it("maps session stop results into the RPC envelope", async () => {
		const handler = makeDaemonSessionStopRpcHandler({
			stop: (request) => {
				expect(request.issueId).toBe("task-1")
				expect(request.projectPath).toBe("/tmp/project")
				return Effect.succeed(sessionSnapshot)
			},
		})

		const result = await Effect.runPromise(
			handler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				projectPath: "/tmp/project",
			}),
		)

		expect(result.session).toEqual(sessionSnapshot)
		expect(result.rpcProtocolVersion).toBe(DAEMON_RPC_PROTOCOL_VERSION)
		expect(typeof result.capturedAtMs).toBe("number")
	})

	it("maps session pause results into the RPC envelope", async () => {
		const handler = makeDaemonSessionPauseRpcHandler({
			pause: (request) => {
				expect(request.issueId).toBe("task-1")
				expect(request.projectPath).toBe("/tmp/project")
				return Effect.succeed(sessionSnapshot)
			},
		})

		const result = await Effect.runPromise(
			handler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				projectPath: "/tmp/project",
			}),
		)

		expect(result.session.state).toBe("busy")
	})

	it("maps session resume results into the RPC envelope", async () => {
		const handler = makeDaemonSessionResumeRpcHandler({
			resume: (request) => {
				expect(request.issueId).toBe("task-1")
				expect(request.projectPath).toBe("/tmp/project")
				return Effect.succeed(sessionSnapshot)
			},
		})

		const result = await Effect.runPromise(
			handler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				projectPath: "/tmp/project",
			}),
		)

		expect(result.session.tmuxSessionName).toBe("az-task-1")
	})

	it("maps session recover results into the RPC envelope", async () => {
		const handler = makeDaemonSessionRecoverRpcHandler({
			recover: (request) => {
				expect(request.issueId).toBe("task-1")
				expect(request.projectPath).toBe("/tmp/project")
				return Effect.succeed(sessionSnapshot)
			},
		})

		const result = await Effect.runPromise(
			handler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				projectPath: "/tmp/project",
			}),
		)

		expect(result.session.worktreePath).toBe("/tmp/project/.worktrees/task-1")
	})

	it("maps session errors into daemon rpc action errors", async () => {
		const handler = makeDaemonSessionStartRpcHandler({
			start: () =>
				Effect.fail(
					new DaemonSessionError({
						reason: "session-limit",
						message: "Maximum session limit reached",
					}),
				),
		})

		const exit = await Effect.runPromiseExit(
			handler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				projectPath: "/tmp/project",
			}),
		)

		expect(exit._tag).toBe("Failure")
		if (exit._tag === "Failure" && exit.cause._tag === "Fail") {
			expect(exit.cause.error).toEqual({
				_tag: "DaemonRpcActionError",
				action: "sessionStart",
				code: "session-limit",
				message: "Maximum session limit reached",
			})
		}
	})
})

describe("makeDaemonSessionUpdateStateRpcHandler", () => {
	it("persists daemon session metadata updates through the recovery service", async () => {
		const handler = makeDaemonSessionUpdateStateRpcHandler({
			updateState: (update) => {
				expect(update.issueId).toBe("task-1")
				expect(update.tmuxSessionName).toBe("az-task-1")
				return Effect.succeed({
					issueId: update.issueId,
					state: update.state,
					projectPath: update.projectPath,
					tmuxSessionName: update.tmuxSessionName ?? "az-task-1",
					worktreePath: update.worktreePath ?? null,
					startedAt: update.startedAt ?? null,
				})
			},
		})

		const result = await Effect.runPromise(
			handler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				state: "busy",
				projectPath: "/tmp/project",
				tmuxSessionName: "az-task-1",
				worktreePath: "/tmp/project/.worktrees/task-1",
				startedAt: "2026-03-20T02:00:00.000Z",
			}),
		)

		expect(result.session).toEqual({
			issueId: "task-1",
			state: "busy",
			projectPath: "/tmp/project",
			tmuxSessionName: "az-task-1",
			worktreePath: "/tmp/project/.worktrees/task-1",
			startedAt: "2026-03-20T02:00:00.000Z",
		})
	})
})

describe("PR rpc handlers", () => {
	it("maps PR workflow handlers into rpc envelopes", async () => {
		const createHandler = makeDaemonPrCreateRpcHandler({
			create: (issueId, projectPath) => {
				expect(issueId).toBe("task-1")
				expect(projectPath).toBe("/tmp/project")
				return Effect.succeed({
					number: 42,
					url: "https://example.com/pr/42",
					title: "[task] Board task (task-1)",
					state: "open",
					draft: true,
					branch: "author/task-1/board-task",
				})
			},
		})
		const cleanupHandler = makeDaemonPrCleanupRpcHandler({
			cleanup: ({ issueId, closeIssue }) => {
				expect(issueId).toBe("task-1")
				expect(closeIssue).toBe(true)
				return Effect.void
			},
		})
		const mergeHandler = makeDaemonPrMergeToMainRpcHandler({
			mergeToMain: ({ issueId }) => {
				expect(issueId).toBe("task-1")
				return Effect.void
			},
		})
		const updateFromBaseHandler = makeDaemonPrUpdateFromBaseRpcHandler({
			updateFromBase: ({ issueId, projectPath }) => {
				expect(issueId).toBe("task-1")
				expect(projectPath).toBe("/tmp/project")
				return Effect.void
			},
		})
		const mergeBaseIntoBranchHandler = makeDaemonPrMergeBaseIntoBranchRpcHandler({
			mergeBaseIntoBranch: ({ issueId, projectPath }) => {
				expect(issueId).toBe("task-1")
				expect(projectPath).toBe("/tmp/project")
				return Effect.void
			},
		})
		const abortMergeHandler = makeDaemonPrAbortMergeRpcHandler({
			abortMerge: ({ issueId, projectPath }) => {
				expect(issueId).toBe("task-1")
				expect(projectPath).toBe("/tmp/project")
				return Effect.void
			},
		})
		const checkHandler = makeDaemonPrCheckGhCliRpcHandler({
			checkGhCli: () => Effect.succeed(true),
		})

		const createResult = await Effect.runPromise(
			createHandler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				projectPath: "/tmp/project",
			}),
		)
		const cleanupResult = await Effect.runPromise(
			cleanupHandler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				projectPath: "/tmp/project",
				closeIssue: true,
			}),
		)
		const mergeResult = await Effect.runPromise(
			mergeHandler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				projectPath: "/tmp/project",
			}),
		)
		const checkResult = await Effect.runPromise(
			checkHandler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		)
		const updateFromBaseResult = await Effect.runPromise(
			updateFromBaseHandler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				projectPath: "/tmp/project",
			}),
		)
		const mergeBaseIntoBranchResult = await Effect.runPromise(
			mergeBaseIntoBranchHandler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				projectPath: "/tmp/project",
			}),
		)
		const abortMergeResult = await Effect.runPromise(
			abortMergeHandler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				projectPath: "/tmp/project",
			}),
		)

		expect(createResult.pullRequest.number).toBe(42)
		expect(cleanupResult.cleanedUp).toBe(true)
		expect(mergeResult.merged).toBe(true)
		expect(checkResult.available).toBe(true)
		expect(updateFromBaseResult.updated).toBe(true)
		expect(mergeBaseIntoBranchResult.merged).toBe(true)
		expect(abortMergeResult.aborted).toBe(true)
	})

	it("maps PR workflow failures into daemon rpc action errors", async () => {
		const handler = makeDaemonPrCreateRpcHandler({
			create: () =>
				Effect.fail(
					new DaemonPrError({
						reason: "worktree-missing",
						message: "No worktree found for task-1",
					}),
				),
		})

		const exit = await Effect.runPromiseExit(
			handler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				projectPath: "/tmp/project",
			}),
		)

		expect(exit._tag).toBe("Failure")
		if (exit._tag === "Failure" && exit.cause._tag === "Fail") {
			expect(exit.cause.error).toEqual({
				_tag: "DaemonRpcActionError",
				action: "prCreate",
				code: "worktree-missing",
				message: "No worktree found for task-1",
			})
		}
	})

	it("maps typed merge conflicts into daemon rpc action errors", async () => {
		const handler = makeDaemonPrUpdateFromBaseRpcHandler({
			updateFromBase: () =>
				Effect.fail(
					new DaemonPrError({
						reason: "merge-conflict",
						message: "Merge conflicts detected in: src/app.ts",
					}),
				),
		})

		const exit = await Effect.runPromiseExit(
			handler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				projectPath: "/tmp/project",
			}),
		)

		expect(exit._tag).toBe("Failure")
		if (exit._tag === "Failure" && exit.cause._tag === "Fail") {
			expect(exit.cause.error).toEqual({
				_tag: "DaemonRpcActionError",
				action: "prUpdateFromBase",
				code: "merge-conflict",
				message: "Merge conflicts detected in: src/app.ts",
			})
		}
	})
})

describe("attachment rpc handlers", () => {
	it("maps attachment list and count results into rpc envelopes", async () => {
		const listHandler = makeDaemonAttachmentListRpcHandler({
			list: (issueId, projectPath) => {
				expect(issueId).toBe("task-1")
				expect(projectPath).toBe("/tmp/project")
				return Effect.succeed([imageAttachment])
			},
		})
		const countHandler = makeDaemonAttachmentCountBatchRpcHandler({
			countBatch: (issueIds, projectPath) => {
				expect(issueIds).toEqual(["task-1", "task-2"])
				expect(projectPath).toBe("/tmp/project")
				return Effect.succeed({
					"task-1": 1,
					"task-2": 0,
				})
			},
		})

		const listResult = await Effect.runPromise(
			listHandler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				projectPath: "/tmp/project",
			}),
		)
		const countResult = await Effect.runPromise(
			countHandler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueIds: ["task-1", "task-2"],
				projectPath: "/tmp/project",
			}),
		)

		expect(listResult.attachments).toEqual([imageAttachment])
		expect(countResult.counts["task-1"]).toBe(1)
	})

	it("maps attachment mutation and path handlers into rpc envelopes", async () => {
		const attachFileHandler = makeDaemonAttachmentAttachFileRpcHandler({
			attachFile: (request) => {
				expect(request.filePath).toBe("/tmp/source.png")
				return Effect.succeed(imageAttachment)
			},
		})
		const attachClipboardHandler = makeDaemonAttachmentAttachClipboardRpcHandler({
			attachClipboard: (request) => {
				expect(request.base64Content).toBe("aGVsbG8=")
				expect(request.mimeType).toBe("image/png")
				return Effect.succeed({
					...imageAttachment,
					originalPath: "clipboard",
				})
			},
		})
		const removeHandler = makeDaemonAttachmentRemoveRpcHandler({
			remove: (request) => {
				expect(request.attachmentId).toBe("att-1")
				return Effect.void
			},
		})
		const materializeHandler = makeDaemonAttachmentMaterializePathRpcHandler({
			materializePath: (request) => {
				expect(request.issueId).toBe("task-1")
				return Effect.succeed("/tmp/project/.azedarach/tmp/attachments/task-1/att-1/att-1.png")
			},
		})

		const attachFileResult = await Effect.runPromise(
			attachFileHandler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				filePath: "/tmp/source.png",
				projectPath: "/tmp/project",
			}),
		)
		const attachClipboardResult = await Effect.runPromise(
			attachClipboardHandler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				base64Content: "aGVsbG8=",
				filename: "clipboard.png",
				mimeType: "image/png",
				projectPath: "/tmp/project",
			}),
		)
		const removeResult = await Effect.runPromise(
			removeHandler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				attachmentId: "att-1",
				projectPath: "/tmp/project",
			}),
		)
		const materializeResult = await Effect.runPromise(
			materializeHandler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				attachmentId: "att-1",
				projectPath: "/tmp/project",
			}),
		)

		expect(attachFileResult.attachment).toEqual(imageAttachment)
		expect(attachClipboardResult.attachment.originalPath).toBe("clipboard")
		expect(removeResult.removed).toBe(true)
		expect(materializeResult.path).toContain(".azedarach/tmp/attachments")
	})

	it("maps attachment daemon errors into rpc action errors", async () => {
		const handler = makeDaemonAttachmentAttachFileRpcHandler({
			attachFile: () =>
				Effect.fail(
					new DaemonAttachmentError({
						reason: "storage",
						message: "attachment sqlite failed",
					}),
				),
		})

		const exit = await Effect.runPromiseExit(
			handler({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "task-1",
				filePath: "/tmp/source.png",
				projectPath: "/tmp/project",
			}),
		)

		expect(exit._tag).toBe("Failure")
		if (exit._tag === "Failure" && exit.cause._tag === "Fail") {
			expect(exit.cause.error).toEqual({
				_tag: "DaemonRpcActionError",
				action: "attachmentAttachFile",
				code: "storage",
				message: "attachment sqlite failed",
			})
		}
	})
})
