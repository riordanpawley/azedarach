import { Database } from "bun:sqlite"
import { getProjectStoragePaths } from "@azedarach/config"
import type { ImageAttachment } from "@azedarach/shared/rpc"
import { FileSystem, Path } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Data, Effect } from "effect"
import { TrackerIssueDaemonService } from "./TrackerIssueDaemonService.js"

interface IssueAttachmentRow {
	readonly issue_id: string
	readonly attachment_id: string
	readonly filename: string
	readonly original_path: string
	readonly mime_type: string
	readonly size_bytes: number
	readonly created_at: string
	readonly content_blob?: Uint8Array
}

interface AttachmentRecord extends ImageAttachment {
	readonly content: Uint8Array
}

export class DaemonAttachmentError extends Data.TaggedError("DaemonAttachmentError")<{
	readonly reason: "invalid-input" | "not-found" | "storage" | "issue-tracker"
	readonly message: string
}> {}

export interface DaemonAttachmentServiceApi {
	readonly list: (
		issueId: string,
		projectPath?: string,
	) => Effect.Effect<ReadonlyArray<ImageAttachment>, DaemonAttachmentError>
	readonly countBatch: (
		issueIds: ReadonlyArray<string>,
		projectPath?: string,
	) => Effect.Effect<Record<string, number>, DaemonAttachmentError>
	readonly attachFile: (params: {
		readonly issueId: string
		readonly filePath: string
		readonly projectPath?: string
	}) => Effect.Effect<ImageAttachment, DaemonAttachmentError>
	readonly attachClipboard: (params: {
		readonly issueId: string
		readonly filename: string
		readonly mimeType: string
		readonly base64Content: string
		readonly projectPath?: string
	}) => Effect.Effect<ImageAttachment, DaemonAttachmentError>
	readonly remove: (params: {
		readonly issueId: string
		readonly attachmentId: string
		readonly projectPath?: string
	}) => Effect.Effect<void, DaemonAttachmentError>
	readonly materializePath: (params: {
		readonly issueId: string
		readonly attachmentId: string
		readonly projectPath?: string
	}) => Effect.Effect<string, DaemonAttachmentError>
}

const ATTACHMENTS_TMP_DIR = ".azedarach/tmp/attachments"
const IMAGE_EXTENSIONS = new Set([".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg"])

const mapStorageError = (message: string): DaemonAttachmentError =>
	new DaemonAttachmentError({
		reason: "storage",
		message,
	})

const mapIssueTrackerError = (message: string): DaemonAttachmentError =>
	new DaemonAttachmentError({
		reason: "issue-tracker",
		message,
	})

const mapInvalidInput = (message: string): DaemonAttachmentError =>
	new DaemonAttachmentError({
		reason: "invalid-input",
		message,
	})

const mapNotFound = (message: string): DaemonAttachmentError =>
	new DaemonAttachmentError({
		reason: "not-found",
		message,
	})

const ensureAttachmentSchema = (database: Database): void => {
	database.run("PRAGMA journal_mode = WAL")
	database.run(
		`CREATE TABLE IF NOT EXISTS issue_attachments (
			issue_id TEXT NOT NULL,
			attachment_id TEXT NOT NULL,
			filename TEXT NOT NULL,
			original_path TEXT NOT NULL,
			mime_type TEXT NOT NULL,
			size_bytes INTEGER NOT NULL,
			created_at TEXT NOT NULL,
			content_blob BLOB NOT NULL,
			PRIMARY KEY (issue_id, attachment_id)
		)`,
	)
	database.run(
		"CREATE INDEX IF NOT EXISTS idx_issue_attachments_issue ON issue_attachments(issue_id, created_at, attachment_id)",
	)
}

const normalizeProjectPath = (projectPath: string | undefined): string =>
	projectPath ?? process.cwd()

const attachmentRowToMetadata = (row: IssueAttachmentRow): ImageAttachment => ({
	id: row.attachment_id,
	filename: row.filename,
	originalPath: row.original_path,
	mimeType: row.mime_type,
	size: row.size_bytes,
	createdAt: row.created_at,
})

const attachmentRowToRecord = (row: IssueAttachmentRow): AttachmentRecord => ({
	...attachmentRowToMetadata(row),
	content: row.content_blob ?? new Uint8Array(),
})

const relativeAttachmentPath = (issueId: string, attachment: ImageAttachment): string =>
	`${ATTACHMENTS_TMP_DIR}/${issueId}/${attachment.id}/${attachment.filename}`

const attachmentLinePrefix = (attachment: ImageAttachment): string => `📎 [${attachment.filename}]`

const buildAttachmentLine = (issueId: string, attachment: ImageAttachment): string => {
	const timestamp = new Date(attachment.createdAt).toLocaleString()
	const source = attachment.originalPath === "clipboard" ? "clipboard" : "file"
	return `${attachmentLinePrefix(attachment)}(${relativeAttachmentPath(issueId, attachment)}) (${source}, ${timestamp})`
}

const makeAttachmentId = (): string => crypto.randomUUID().replaceAll("-", "")

const resolveExtension = (filename: string): string => {
	const dotIndex = filename.lastIndexOf(".")
	return dotIndex >= 0 ? filename.slice(dotIndex).toLowerCase() : ""
}

const requireImageExtension = (filename: string): Effect.Effect<string, DaemonAttachmentError> => {
	const extension = resolveExtension(filename)
	return IMAGE_EXTENSIONS.has(extension)
		? Effect.succeed(extension)
		: Effect.fail(
				mapInvalidInput(
					`Not a supported image format: ${filename}. Supported: ${[...IMAGE_EXTENSIONS].join(", ")}`,
				),
			)
}

export class DaemonAttachmentService extends Effect.Service<DaemonAttachmentService>()(
	"DaemonAttachmentService",
	{
		dependencies: [BunContext.layer, TrackerIssueDaemonService.Default],
		effect: Effect.gen(function* () {
			const fs = yield* FileSystem.FileSystem
			const pathService = yield* Path.Path
			const trackerIssues = yield* TrackerIssueDaemonService

			const resolveDbPath = (projectPath?: string) =>
				Effect.gen(function* () {
					const resolvedProjectPath = normalizeProjectPath(projectPath)
					const storagePaths = getProjectStoragePaths(resolvedProjectPath, pathService)
					yield* fs
						.makeDirectory(storagePaths.storageDirectory, { recursive: true })
						.pipe(
							Effect.mapError(() =>
								mapStorageError(
									`Failed to create storage directory ${storagePaths.storageDirectory}`,
								),
							),
						)
					const canonicalExists = yield* fs
						.exists(storagePaths.canonicalDbPath)
						.pipe(Effect.orElseSucceed(() => false))
					const legacyExists = canonicalExists
						? false
						: yield* fs.exists(storagePaths.legacyDbPath).pipe(Effect.orElseSucceed(() => false))
					return canonicalExists
						? storagePaths.canonicalDbPath
						: legacyExists
							? storagePaths.legacyDbPath
							: storagePaths.canonicalDbPath
				})

			const withDatabase = <A>(
				projectPath: string | undefined,
				use: (
					database: Database,
					resolvedProjectPath: string,
				) => Effect.Effect<A, DaemonAttachmentError>,
			): Effect.Effect<A, DaemonAttachmentError> => {
				const resolvedProjectPath = normalizeProjectPath(projectPath)
				return resolveDbPath(projectPath).pipe(
					Effect.flatMap((dbPath) =>
						Effect.acquireUseRelease(
							Effect.try({
								try: () => {
									const database = new Database(dbPath)
									ensureAttachmentSchema(database)
									return database
								},
								catch: () => mapStorageError(`Failed to open sqlite database ${dbPath}`),
							}),
							(database) => use(database, resolvedProjectPath),
							(database) =>
								Effect.sync(() => {
									database.close()
								}).pipe(Effect.ignore),
						),
					),
				)
			}

			const listAttachmentsFromDatabase = (
				database: Database,
				issueId: string,
			): ReadonlyArray<ImageAttachment> =>
				database
					.query<IssueAttachmentRow, [string]>(
						`SELECT
							issue_id,
							attachment_id,
							filename,
							original_path,
							mime_type,
							size_bytes,
							created_at
						FROM issue_attachments
						WHERE issue_id = ?
						ORDER BY created_at ASC, attachment_id ASC`,
					)
					.all(issueId)
					.map(attachmentRowToMetadata)

			const getAttachmentRecord = (
				database: Database,
				issueId: string,
				attachmentId: string,
			): AttachmentRecord | undefined => {
				const row = database
					.query<IssueAttachmentRow, [string, string]>(
						`SELECT
							issue_id,
							attachment_id,
							filename,
							original_path,
							mime_type,
							size_bytes,
							created_at,
							content_blob
						FROM issue_attachments
						WHERE issue_id = ? AND attachment_id = ?
						LIMIT 1`,
					)
					.get(issueId, attachmentId)
				return row === null || row === undefined ? undefined : attachmentRowToRecord(row)
			}

			const upsertAttachment = (
				database: Database,
				issueId: string,
				attachment: ImageAttachment,
				content: Uint8Array,
			): void => {
				database
					.prepare(
						`INSERT INTO issue_attachments (
							issue_id,
							attachment_id,
							filename,
							original_path,
							mime_type,
							size_bytes,
							created_at,
							content_blob
						)
						VALUES (?, ?, ?, ?, ?, ?, ?, ?)
						ON CONFLICT(issue_id, attachment_id)
						DO UPDATE SET
							filename = excluded.filename,
							original_path = excluded.original_path,
							mime_type = excluded.mime_type,
							size_bytes = excluded.size_bytes,
							created_at = excluded.created_at,
							content_blob = excluded.content_blob`,
					)
					.run(
						issueId,
						attachment.id,
						attachment.filename,
						attachment.originalPath,
						attachment.mimeType,
						attachment.size,
						attachment.createdAt,
						content,
					)
			}

			const attachmentDirectoryPath = (
				projectRootPath: string,
				issueId: string,
				attachmentId: string,
			): string => pathService.join(projectRootPath, ATTACHMENTS_TMP_DIR, issueId, attachmentId)

			const materializeAttachment = (
				projectRootPath: string,
				issueId: string,
				attachmentId: string,
			): Effect.Effect<string, DaemonAttachmentError> =>
				withDatabase(projectRootPath, (database) =>
					Effect.gen(function* () {
						const attachment = getAttachmentRecord(database, issueId, attachmentId)
						if (attachment === undefined) {
							return yield* Effect.fail(mapNotFound(`Attachment not found: ${attachmentId}`))
						}
						const directory = attachmentDirectoryPath(projectRootPath, issueId, attachment.id)
						yield* fs
							.makeDirectory(directory, { recursive: true })
							.pipe(
								Effect.mapError(() =>
									mapStorageError(`Failed to create attachment directory ${directory}`),
								),
							)
						const filePath = pathService.join(directory, attachment.filename)
						yield* fs
							.writeFile(filePath, attachment.content)
							.pipe(
								Effect.mapError(() =>
									mapStorageError(`Failed to materialize attachment ${attachment.id}`),
								),
							)
						return filePath
					}),
				)

			const appendAttachmentNote = (
				issueId: string,
				attachment: ImageAttachment,
				projectPath?: string,
			): Effect.Effect<void, DaemonAttachmentError> =>
				Effect.gen(function* () {
					const issue = yield* trackerIssues
						.get(issueId, projectPath)
						.pipe(Effect.mapError((error) => mapIssueTrackerError(error.message)))
					const existingNotes = issue.notes ?? ""
					const line = buildAttachmentLine(issueId, attachment)
					const nextNotes = existingNotes.trim().length === 0 ? line : `${existingNotes}\n${line}`
					yield* trackerIssues
						.update(issueId, { notes: nextNotes }, projectPath)
						.pipe(Effect.mapError((error) => mapIssueTrackerError(error.message)))
				})

			const removeAttachmentNote = (
				issueId: string,
				attachment: ImageAttachment,
				projectPath?: string,
			): Effect.Effect<void, DaemonAttachmentError> =>
				Effect.gen(function* () {
					const issue = yield* trackerIssues
						.get(issueId, projectPath)
						.pipe(Effect.mapError((error) => mapIssueTrackerError(error.message)))
					const existingNotes = issue.notes
					if (existingNotes === undefined || existingNotes.length === 0) {
						return
					}
					const originalLines = existingNotes.split("\n")
					const filteredLines = originalLines.filter(
						(line) => !line.startsWith(attachmentLinePrefix(attachment)),
					)
					if (filteredLines.length === originalLines.length) {
						return
					}
					yield* trackerIssues
						.update(issueId, { notes: filteredLines.join("\n") }, projectPath)
						.pipe(Effect.mapError((error) => mapIssueTrackerError(error.message)))
				})

			const attachContent = (params: {
				readonly issueId: string
				readonly filename: string
				readonly originalPath: string
				readonly mimeType: string
				readonly content: Uint8Array
				readonly projectPath?: string
			}): Effect.Effect<ImageAttachment, DaemonAttachmentError> =>
				withDatabase(params.projectPath, (database) =>
					Effect.gen(function* () {
						const extension = yield* requireImageExtension(params.filename)
						const attachment: ImageAttachment = {
							id: makeAttachmentId(),
							filename: `${makeAttachmentId()}${extension}`,
							originalPath: params.originalPath,
							mimeType: params.mimeType,
							size: params.content.byteLength,
							createdAt: new Date().toISOString(),
						}
						yield* Effect.try({
							try: () => {
								upsertAttachment(database, params.issueId, attachment, params.content)
							},
							catch: () => mapStorageError(`Failed to store attachment for ${params.issueId}`),
						})
						yield* appendAttachmentNote(params.issueId, attachment, params.projectPath)
						return attachment
					}),
				)

			return {
				list: (issueId, projectPath) =>
					withDatabase(projectPath, (database) =>
						Effect.try({
							try: () => listAttachmentsFromDatabase(database, issueId),
							catch: () => mapStorageError(`Failed to list attachments for ${issueId}`),
						}),
					),
				countBatch: (issueIds, projectPath) =>
					withDatabase(projectPath, (database) =>
						Effect.try({
							try: () => {
								const counts: Record<string, number> = {}
								for (const issueId of issueIds) {
									counts[issueId] = listAttachmentsFromDatabase(database, issueId).length
								}
								return counts
							},
							catch: () => mapStorageError("Failed to count attachments"),
						}),
					),
				attachFile: ({ issueId, filePath, projectPath }) =>
					Effect.gen(function* () {
						const filename = pathService.basename(filePath)
						yield* requireImageExtension(filename)
						const content = yield* fs
							.readFile(filePath)
							.pipe(
								Effect.mapError(() =>
									mapStorageError(`Failed to read attachment file ${filePath}`),
								),
							)
						const mimeType = resolveExtension(filename) === ".svg" ? "image/svg+xml" : "image/png"
						return yield* attachContent({
							issueId,
							filename,
							originalPath: filePath,
							mimeType,
							content,
							projectPath,
						})
					}),
				attachClipboard: ({ issueId, filename, mimeType, base64Content, projectPath }) =>
					Effect.gen(function* () {
						yield* requireImageExtension(filename)
						const content = yield* Effect.try({
							try: () => new Uint8Array(Buffer.from(base64Content, "base64")),
							catch: () => mapInvalidInput("Clipboard payload was not valid base64."),
						})
						return yield* attachContent({
							issueId,
							filename,
							originalPath: "clipboard",
							mimeType,
							content,
							projectPath,
						})
					}),
				remove: ({ issueId, attachmentId, projectPath }) =>
					withDatabase(projectPath, (database, resolvedProjectPath) =>
						Effect.gen(function* () {
							const attachment = getAttachmentRecord(database, issueId, attachmentId)
							if (attachment === undefined) {
								return yield* Effect.fail(mapNotFound(`Attachment not found: ${attachmentId}`))
							}
							yield* Effect.try({
								try: () => {
									database
										.prepare(
											"DELETE FROM issue_attachments WHERE issue_id = ? AND attachment_id = ?",
										)
										.run(issueId, attachmentId)
								},
								catch: () =>
									mapStorageError(`Failed to remove attachment ${attachmentId} from sqlite`),
							})
							yield* fs
								.remove(attachmentDirectoryPath(resolvedProjectPath, issueId, attachmentId), {
									recursive: true,
								})
								.pipe(Effect.ignore)
							yield* removeAttachmentNote(issueId, attachment, projectPath)
						}),
					),
				materializePath: ({ issueId, attachmentId, projectPath }) =>
					materializeAttachment(normalizeProjectPath(projectPath), issueId, attachmentId),
			} satisfies DaemonAttachmentServiceApi
		}),
	},
) {}
