import { AppConfig } from "@azedarach/config"
import { DaemonRpcClient } from "@azedarach/shared/rpc"
import { FileSystem } from "@effect/platform"
import { Data, Effect } from "effect"
import type { Issue } from "../contracts.js"
import { clearActiveEditorPopup, setActiveEditorPopup } from "../lib/editorPopupState.js"
import {
	createBlankIssueTemplate,
	type ParseMarkdownError,
	parseMarkdownToIssue,
	parseMarkdownToNewIssue,
	serializeIssueToMarkdown,
} from "../utils/issueEditorMarkdown.js"
import {
	resolveIssueCreateImplementations,
	resolveIssueEditorDefaultImplementation,
} from "../utils/issueImplementations.js"
import { getTuiProjectContextRead } from "./TuiProjectContextService.js"

export { ParseMarkdownError } from "../utils/issueEditorMarkdown.js"

export class EditorError extends Data.TaggedError("EditorError")<{
	readonly message: string
}> {}

export interface CreatedIssue {
	readonly id: string
	readonly title: string
}

export interface IssueEditorServiceApi {
	readonly editIssue: (issue: Issue) => Effect.Effect<void, ParseMarkdownError | EditorError>
	readonly createIssue: () => Effect.Effect<CreatedIssue, ParseMarkdownError | EditorError>
}

const runEditorPopup = (params: {
	readonly title: string
	readonly tempFile: string
}): Effect.Effect<void, EditorError> =>
	Effect.try({
		try: () => {
			const editor = process.env.EDITOR ?? "vim"
			const channel = `az-editor-${Date.now()}`
			setActiveEditorPopup({ channel, tempFile: params.tempFile })

			const displayResult = Bun.spawnSync(
				[
					"tmux",
					"display-popup",
					"-E",
					"-w",
					"90%",
					"-h",
					"90%",
					"-T",
					params.title,
					"--",
					"sh",
					"-c",
					`${editor} "${params.tempFile}"; tmux wait-for -S ${channel}`,
				],
				{ stdin: "inherit", stdout: "inherit", stderr: "inherit" },
			)
			if (!displayResult.success) {
				throw new Error(`tmux display-popup failed with exit code ${displayResult.exitCode}`)
			}

			const waitResult = Bun.spawnSync(["tmux", "wait-for", channel], {
				stdin: "inherit",
				stdout: "inherit",
				stderr: "inherit",
			})
			if (!waitResult.success) {
				throw new Error(`tmux wait-for failed with exit code ${waitResult.exitCode}`)
			}
		},
		catch: (error) =>
			new EditorError({
				message: error instanceof Error ? error.message : String(error),
			}),
	}).pipe(Effect.ensuring(Effect.sync(clearActiveEditorPopup)))

const removeTempFile = (fs: FileSystem.FileSystem, tempFile: string): Effect.Effect<void, never> =>
	Effect.ignoreLogged(
		fs.remove(tempFile).pipe(
			Effect.mapError(
				(error) =>
					new EditorError({
						message: `Failed to remove temp file: ${String(error)}`,
					}),
			),
		),
	)

export class IssueEditorService extends Effect.Service<IssueEditorService>()("IssueEditorService", {
	effect: Effect.gen(function* () {
		const appConfig = yield* AppConfig
		const daemonRpcClient = yield* DaemonRpcClient
		const projectContext = yield* getTuiProjectContextRead
		const fs = yield* FileSystem.FileSystem

		const getProjectPath = (): Effect.Effect<string> =>
			projectContext
				.getCurrentPath()
				.pipe(Effect.map((projectPath) => projectPath ?? process.cwd()))

		const service: IssueEditorServiceApi = {
			editIssue: (issue) =>
				Effect.gen(function* () {
					const projectPath = yield* getProjectPath()
					const tempFile = `/tmp/azedarach-${issue.id}.md`
					yield* fs.writeFileString(tempFile, serializeIssueToMarkdown(issue)).pipe(
						Effect.mapError(
							(error) =>
								new EditorError({
									message: `Failed to write temp file: ${String(error)}`,
								}),
						),
					)

					yield* runEditorPopup({ title: ` Edit: ${issue.id} `, tempFile })
					const editedMarkdown = yield* fs.readFileString(tempFile).pipe(
						Effect.mapError(
							(error) =>
								new EditorError({
									message: `Failed to read edited file: ${String(error)}`,
								}),
						),
					)
					const updates = yield* parseMarkdownToIssue(editedMarkdown, issue)

					if (Object.keys(updates).length === 0) {
						return
					}

					if (updates.type !== undefined) {
						yield* Effect.fail(
							new EditorError({
								message: "Type changes not yet implemented. Please use tracker CLI directly.",
							}),
						)
					}

					yield* daemonRpcClient
						.issueUpdate({
							issueId: issue.id,
							projectPath,
							patch: {
								status: updates.status,
								notes: updates.notes,
								priority: updates.priority,
								title: updates.title,
								description: updates.description,
								design: updates.design,
								acceptance: updates.acceptance,
								assignee: updates.assignee,
								estimate: updates.estimate,
								labels: updates.labels === undefined ? undefined : [...updates.labels],
								implementations:
									updates.implementations === undefined ? undefined : [...updates.implementations],
							},
						})
						.pipe(
							Effect.mapError(
								(error) =>
									new EditorError({
										message: `Failed to update issue: ${error.message}`,
									}),
							),
						)
				}).pipe(Effect.ensuring(removeTempFile(fs, `/tmp/azedarach-${issue.id}.md`))),

			createIssue: () =>
				Effect.gen(function* () {
					const projectPath = yield* getProjectPath()
					const issueEditorConfig = yield* appConfig.getIssueEditorConfig()
					const registry = yield* daemonRpcClient.implementationGetRegistry({ projectPath }).pipe(
						Effect.map((result) => result.registry),
						Effect.mapError(
							(error) =>
								new EditorError({
									message: `Failed to load implementation registry: ${error.message}`,
								}),
						),
					)
					const defaultImplementation = resolveIssueEditorDefaultImplementation(
						registry,
						issueEditorConfig.defaultImplementation,
					)
					const availableImplementations = registry.implementations.map(
						(implementation) => implementation.name,
					)

					const tempFile = "/tmp/azedarach-new.md"
					yield* fs
						.writeFileString(
							tempFile,
							createBlankIssueTemplate(defaultImplementation, availableImplementations),
						)
						.pipe(
							Effect.mapError(
								(error) =>
									new EditorError({
										message: `Failed to write temp file: ${String(error)}`,
									}),
							),
						)

					yield* runEditorPopup({ title: " Create New Bead ", tempFile })
					const editedMarkdown = yield* fs.readFileString(tempFile).pipe(
						Effect.mapError(
							(error) =>
								new EditorError({
									message: `Failed to read edited file: ${String(error)}`,
								}),
						),
					)
					const fields = yield* parseMarkdownToNewIssue(editedMarkdown)
					const implementations = resolveIssueCreateImplementations(registry, {
						requestedImplementations: fields.implementations,
						configuredDefaultImplementation: issueEditorConfig.defaultImplementation,
					})
					const createdIssue = yield* daemonRpcClient
						.issueCreate({
							projectPath,
							input: {
								title: fields.title,
								type: fields.type,
								priority: fields.priority,
								description: fields.description,
								design: fields.design,
								acceptance: fields.acceptance,
								assignee: fields.assignee,
								estimate: fields.estimate,
								labels: fields.labels === undefined ? undefined : [...fields.labels],
								implementations: [...implementations],
							},
						})
						.pipe(
							Effect.mapError(
								(error) =>
									new EditorError({
										message: `Failed to create issue: ${error.message}`,
									}),
							),
						)

					if (
						(fields.status.length > 0 && fields.status !== "open") ||
						fields.notes !== undefined
					) {
						yield* daemonRpcClient
							.issueUpdate({
								issueId: createdIssue.issue.id,
								projectPath,
								patch: {
									status: fields.status !== "open" ? fields.status : undefined,
									notes: fields.notes,
								},
							})
							.pipe(
								Effect.mapError(
									(error) =>
										new EditorError({
											message: `Failed to update issue status/notes: ${error.message}`,
										}),
								),
							)
					}

					return {
						id: createdIssue.issue.id,
						title: createdIssue.issue.title,
					}
				}).pipe(Effect.ensuring(removeTempFile(fs, "/tmp/azedarach-new.md"))),
		}

		return service
	}),
}) {}
