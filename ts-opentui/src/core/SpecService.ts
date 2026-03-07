import { Data, Effect, Fiber, Ref, SubscriptionRef } from "effect"
import { AppConfig } from "../config/AppConfig.js"
import { ProjectService } from "../services/ProjectService.js"
import { LinearSdk } from "./LinearSdk.js"
import { LocalIssueStore, type LocalIssueStoreError } from "./LocalIssueStore.js"
import type {
	SpecCoverageReport,
	SpecIssueLink,
	SpecIssueRef,
	SpecLinkType,
	SpecPublishConfig,
	SpecPublishDocumentOutcome,
	SpecPublishOutcome,
	SpecRequirement,
	SpecRequirementRef,
	SpecRequirementWithStats,
} from "./specTypes.js"

export class SpecServiceError extends Data.TaggedError("SpecServiceError")<{
	readonly message: string
	readonly cause?: unknown
}> {}

const managedMarkerStart = (key: string): string => `<!-- AZ-SPEC:${key}:START -->`
const managedMarkerEnd = (key: string): string => `<!-- AZ-SPEC:${key}:END -->`

export const upsertManagedSection = (
    existingContent: string | null | undefined,
    key: string,
    renderedContent: string,
): string => {
	const start = managedMarkerStart(key)
	const end = managedMarkerEnd(key)
	const managed = `${start}\n${renderedContent}\n${end}`
	const source = existingContent ?? ""

	const startIndex = source.indexOf(start)
	const endIndex = source.indexOf(end)
	if (startIndex >= 0 && endIndex >= 0 && endIndex > startIndex) {
		return (
			source.slice(0, startIndex).trimEnd() +
			(source.slice(0, startIndex).trimEnd().length > 0 ? "\n\n" : "") +
			managed +
			(source.slice(endIndex + end.length).trimStart().length > 0
				? `\n\n${source.slice(endIndex + end.length).trimStart()}`
				: "")
		)
	}

	if (source.trim().length === 0) {
		return managed
	}

	return `${source.trimEnd()}\n\n${managed}`
}

const toErrorMessage = (error: unknown): string => {
    if (error instanceof SpecServiceError) {
        return error.message
    }
    if (error instanceof Error) {
        return error.message
    }
    return String(error)
}

const formatLinkSummary = (
	links: readonly SpecIssueLink[],
	requirementId: string,
): {
	readonly total: number
	readonly implementsCount: number
	readonly testsCount: number
} => {
	const requirementLinks = links.filter((link) => link.requirement_id === requirementId)
	return {
		total: requirementLinks.length,
		implementsCount: requirementLinks.filter((link) => link.link_type === "implements").length,
		testsCount: requirementLinks.filter((link) => link.link_type === "tests").length,
	}
}

export interface SpecServiceApi {
	readonly listRequirements: (cwd?: string) => Effect.Effect<readonly SpecRequirement[], SpecServiceError>
	readonly listRequirementsWithStats: (
		cwd?: string,
	) => Effect.Effect<readonly SpecRequirementWithStats[], SpecServiceError>
	readonly getRequirement: (
		id: string,
		cwd?: string,
	) => Effect.Effect<SpecRequirement | undefined, SpecServiceError>
	readonly createRequirement: (
		params: {
			id: string
			title: string
			body: string
			kind?: "functional" | "acceptance" | "other"
			status?: string
			priority?: number
		},
		cwd?: string,
	) => Effect.Effect<SpecRequirement, SpecServiceError>
	readonly updateRequirement: (
		id: string,
		fields: {
			title?: string
			body?: string
			kind?: "functional" | "acceptance" | "other"
			status?: string
			priority?: number
		},
		cwd?: string,
	) => Effect.Effect<boolean, SpecServiceError>
	readonly deleteRequirement: (id: string, cwd?: string) => Effect.Effect<boolean, SpecServiceError>
	readonly listLinks: (
		filters?: {
			issueId?: string
			requirementId?: string
		},
		cwd?: string,
	) => Effect.Effect<readonly SpecIssueLink[], SpecServiceError>
	readonly addIssueLink: (
		issueId: string,
		requirementId: string,
		linkType: SpecLinkType,
		cwd?: string,
	) => Effect.Effect<void, SpecServiceError>
	readonly removeIssueLink: (
		issueId: string,
		requirementId: string,
		linkType?: SpecLinkType,
		cwd?: string,
	) => Effect.Effect<number, SpecServiceError>
	readonly listIssueRequirements: (
		issueId: string,
		cwd?: string,
	) => Effect.Effect<readonly SpecRequirementRef[], SpecServiceError>
	readonly listRequirementIssues: (
		requirementId: string,
		cwd?: string,
	) => Effect.Effect<readonly SpecIssueRef[], SpecServiceError>
	readonly getCoverageReport: (cwd?: string) => Effect.Effect<SpecCoverageReport, SpecServiceError>
	readonly getPublishConfig: (cwd?: string) => Effect.Effect<SpecPublishConfig, SpecServiceError>
	readonly setPublishConfig: (
		config: SpecPublishConfig,
		cwd?: string,
	) => Effect.Effect<void, SpecServiceError>
	readonly getLastPublishOutcome: (
		cwd?: string,
	) => Effect.Effect<SpecPublishOutcome | undefined, SpecServiceError>
	readonly publish: (cwd?: string) => Effect.Effect<SpecPublishOutcome, SpecServiceError>
	readonly scheduleAutoPublish: (
		reason: string,
		cwd?: string,
	) => Effect.Effect<void, SpecServiceError>
}

export class SpecService extends Effect.Service<SpecService>()("SpecService", {
	dependencies: [LocalIssueStore.Default, LinearSdk.Default, AppConfig.Default, ProjectService.Default],
	effect: Effect.gen(function* () {
		const localIssueStore = yield* LocalIssueStore
		const linearSdk = yield* LinearSdk
		const appConfig = yield* AppConfig
		const projectService = yield* ProjectService
		const pendingPublishFibers = yield* Ref.make(
			new Map<string, Fiber.RuntimeFiber<void, SpecServiceError>>(),
		)

		const resolveEffectiveCwd = (cwd?: string): Effect.Effect<string> =>
			cwd
				? Effect.succeed(cwd)
				: projectService.getCurrentPath().pipe(Effect.map((projectPath) => projectPath ?? process.cwd()))

		const fromStore = <A>(
			operation: string,
			effect: Effect.Effect<A, LocalIssueStoreError>,
		): Effect.Effect<A, SpecServiceError> =>
			effect.pipe(
				Effect.mapError(
					(error) =>
						new SpecServiceError({
							message: `Spec store ${operation} failed: ${error.message}`,
							cause: error,
						}),
				),
			)

		const resolveProjectReference = (
			config: SpecPublishConfig,
		): Effect.Effect<string, SpecServiceError> =>
			Effect.gen(function* () {
				if (config.target_project && config.target_project.trim().length > 0) {
					return config.target_project.trim()
				}

				const currentConfig = yield* SubscriptionRef.get(appConfig.config)
				if ("linear" in currentConfig.issueTracker) {
					const configuredProject = currentConfig.issueTracker.linear.project
					if (configuredProject && configuredProject.trim().length > 0) {
						return configuredProject.trim()
					}
				}

				return yield* Effect.fail(
					new SpecServiceError({
						message:
							"No publish target project configured. Set target via `az spec publish config set --project ...`.",
					}),
				)
			})

		const renderPublishDocuments = (
			requirements: readonly SpecRequirement[],
			links: readonly SpecIssueLink[],
		): readonly {
			key: "overview" | "requirements" | "acceptance" | "change_log"
			title: string
			content: string
		}[] => {
			const totalRequirements = requirements.length
			const totalLinks = links.length
			const generatedAt = new Date().toISOString()

			const overview = [
				"# Spec Overview",
				"",
				`Generated at: ${generatedAt}`,
				"",
				`- Requirements: ${totalRequirements}`,
				`- Links: ${totalLinks}`,
			].join("\n")

			const requirementsBody = [
				"# Requirements Index",
				"",
				...requirements
					.filter((requirement) => requirement.kind !== "acceptance")
					.flatMap((requirement) => {
						const summary = formatLinkSummary(links, requirement.id)
						return [
							`## ${requirement.id} ${requirement.title}`,
							`- Status: ${requirement.status}`,
							`- Priority: ${requirement.priority}`,
							`- Linked issues: ${summary.total}`,
							`- Implements links: ${summary.implementsCount}`,
							`- Tests links: ${summary.testsCount}`,
							"",
							requirement.body,
							"",
						]
					}),
			].join("\n")

			const acceptanceBody = [
				"# Acceptance Index",
				"",
				...requirements
					.filter((requirement) => requirement.kind === "acceptance")
					.flatMap((requirement) => {
						const summary = formatLinkSummary(links, requirement.id)
						return [
							`## ${requirement.id} ${requirement.title}`,
							`- Linked issues: ${summary.total}`,
							"",
							requirement.body,
							"",
						]
					}),
			].join("\n")

			const changeLog = [
				"# Change Log",
				"",
				`- ${generatedAt}: Published ${totalRequirements} requirements and ${totalLinks} links.`,
			].join("\n")

			return [
				{ key: "overview", title: "Spec Overview", content: overview },
				{ key: "requirements", title: "Requirements Index", content: requirementsBody },
				{ key: "acceptance", title: "Acceptance Index", content: acceptanceBody },
				{ key: "change_log", title: "Change Log", content: changeLog },
			]
		}

		const runPublish = (cwd?: string): Effect.Effect<SpecPublishOutcome, SpecServiceError> =>
			Effect.gen(function* () {
				const effectiveCwd = yield* resolveEffectiveCwd(cwd)
				const [requirements, links, config] = yield* Effect.all([
					fromStore("listSpecRequirements", localIssueStore.listSpecRequirements(effectiveCwd)),
					fromStore("listSpecIssueLinks", localIssueStore.listSpecIssueLinks(undefined, effectiveCwd)),
					fromStore("getSpecPublishConfig", localIssueStore.getSpecPublishConfig(effectiveCwd)),
				])

				const startedAt = new Date().toISOString()
				const requirementCount = requirements.length
				const linkCount = links.length
				const renderedDocs = renderPublishDocuments(requirements, links).map((document) => ({
					...document,
					title: config.documents[document.key],
				}))

				const projectReference = yield* resolveProjectReference(config)
				const projectId = yield* linearSdk.resolveProjectId(projectReference).pipe(
					Effect.mapError(
						(error) =>
							new SpecServiceError({
								message: `Unable to resolve publish project '${projectReference}': ${error.message}`,
								cause: error,
							}),
					),
				)

				const allDocuments = yield* linearSdk.documents({ first: 250 }).pipe(
					Effect.mapError(
						(error) =>
							new SpecServiceError({
								message: `Unable to list project documents: ${error.message}`,
								cause: error,
							}),
					),
				)

				const outcomes = yield* Effect.all(
					renderedDocs.map((document) =>
						Effect.gen(function* () {
							const existing = allDocuments.nodes.find(
								(candidate) =>
									candidate.projectId === projectId &&
									candidate.title.trim().toLowerCase() === document.title.trim().toLowerCase() &&
									candidate.trashed !== true,
							)
							const mergedContent = upsertManagedSection(
								existing?.content,
								document.key.toUpperCase(),
								document.content,
							)

							if (existing) {
								yield* linearSdk.updateDocument(existing.id, {
									title: document.title,
									content: mergedContent,
								})
							} else {
								yield* linearSdk.createDocument({
									title: document.title,
									content: mergedContent,
									projectId,
								})
							}

							return {
								document_key: document.key,
								title: document.title,
								status: "success",
								message: existing
									? `Updated document '${document.title}'`
									: `Created document '${document.title}'`,
								requirement_count: requirementCount,
								link_count: linkCount,
							} satisfies SpecPublishDocumentOutcome
                        }).pipe(
                            Effect.catchAll((error: unknown) =>
                                Effect.succeed({
                                    document_key: document.key,
                                    title: document.title,
                                    status: "failed",
                                    message: toErrorMessage(error),
                                    requirement_count: requirementCount,
                                    link_count: linkCount,
                                } satisfies SpecPublishDocumentOutcome),
                            ),
                        ),
					),
					{ concurrency: 1 },
				)

				const successCount = outcomes.filter((outcome) => outcome.status === "success").length
				const status: SpecPublishOutcome["status"] =
					successCount === outcomes.length
						? "success"
						: successCount === 0
							? "failed"
							: "partial"
				const finishedAt = new Date().toISOString()
				const outcome: SpecPublishOutcome = {
					started_at: startedAt,
					finished_at: finishedAt,
					status,
					total_requirements: requirementCount,
					total_links: linkCount,
					outcomes,
				}

				yield* fromStore(
					"setSpecPublishOutcome",
					localIssueStore.setSpecPublishOutcome(outcome, effectiveCwd),
				)
				return outcome
			})

		const scheduleAutoPublishInternal = (
			reason: string,
			cwd?: string,
		): Effect.Effect<void, SpecServiceError> =>
			Effect.gen(function* () {
				const effectiveCwd = yield* resolveEffectiveCwd(cwd)
				const config = yield* fromStore(
					"getSpecPublishConfig",
					localIssueStore.getSpecPublishConfig(effectiveCwd),
				)
				if (!config.enabled) {
					return
				}

				const existingFiber = yield* Ref.get(pendingPublishFibers).pipe(
					Effect.map((fibers) => fibers.get(effectiveCwd)),
				)
				if (existingFiber !== undefined) {
					yield* Fiber.interrupt(existingFiber)
				}

				const nextFiber = yield* Effect.fork(
					Effect.sleep(`${Math.max(0, Math.floor(config.debounce_ms))} millis`).pipe(
						Effect.zipRight(runPublish(effectiveCwd)),
						Effect.asVoid,
						Effect.catchAll((error) =>
							Effect.logWarning(
								`Spec auto-publish failed after reason='${reason}': ${error.message}`,
							),
						),
					),
				)

				yield* Ref.update(pendingPublishFibers, (fibers) => {
					const next = new Map(fibers)
					next.set(effectiveCwd, nextFiber)
					return next
				})
			})

		return {
			listRequirements: (cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* resolveEffectiveCwd(cwd)
					return yield* fromStore(
						"listSpecRequirements",
						localIssueStore.listSpecRequirements(effectiveCwd),
					)
				}),
			listRequirementsWithStats: (cwd?: string) =>
				Effect.gen(function* () {
					const report = yield* fromStore(
						"getSpecCoverageReport",
						localIssueStore.getSpecCoverageReport(cwd),
					)
					return report.requirements
				}),
			getRequirement: (id: string, cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* resolveEffectiveCwd(cwd)
					return yield* fromStore(
						"getSpecRequirement",
						localIssueStore.getSpecRequirement(id, effectiveCwd),
					)
				}),
			createRequirement: (
				params: {
					id: string
					title: string
					body: string
					kind?: "functional" | "acceptance" | "other"
					status?: string
					priority?: number
				},
				cwd?: string,
			) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* resolveEffectiveCwd(cwd)
					const created = yield* fromStore(
						"createSpecRequirement",
						localIssueStore.createSpecRequirement(params, effectiveCwd),
					)
					yield* scheduleAutoPublishInternal("requirement_created", effectiveCwd)
					return created
				}),
			updateRequirement: (
				id: string,
				fields: {
					title?: string
					body?: string
					kind?: "functional" | "acceptance" | "other"
					status?: string
					priority?: number
				},
				cwd?: string,
			) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* resolveEffectiveCwd(cwd)
					const updated = yield* fromStore(
						"updateSpecRequirement",
						localIssueStore.updateSpecRequirement(id, fields, effectiveCwd),
					)
					if (updated) {
						yield* scheduleAutoPublishInternal("requirement_updated", effectiveCwd)
					}
					return updated
				}),
			deleteRequirement: (id: string, cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* resolveEffectiveCwd(cwd)
					const deleted = yield* fromStore(
						"deleteSpecRequirement",
						localIssueStore.deleteSpecRequirement(id, effectiveCwd),
					)
					if (deleted) {
						yield* scheduleAutoPublishInternal("requirement_deleted", effectiveCwd)
					}
					return deleted
				}),
			listLinks: (
				filters?: {
					issueId?: string
					requirementId?: string
				},
				cwd?: string,
			) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* resolveEffectiveCwd(cwd)
					return yield* fromStore(
						"listSpecIssueLinks",
						localIssueStore.listSpecIssueLinks(filters, effectiveCwd),
					)
				}),
			addIssueLink: (
				issueId: string,
				requirementId: string,
				linkType: SpecLinkType,
				cwd?: string,
			) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* resolveEffectiveCwd(cwd)
					yield* fromStore(
						"addSpecIssueLink",
						localIssueStore.addSpecIssueLink(issueId, requirementId, linkType, effectiveCwd),
					)
					yield* scheduleAutoPublishInternal("link_added", effectiveCwd)
				}),
			removeIssueLink: (
				issueId: string,
				requirementId: string,
				linkType?: SpecLinkType,
				cwd?: string,
			) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* resolveEffectiveCwd(cwd)
					const removed = yield* fromStore(
						"removeSpecIssueLink",
						localIssueStore.removeSpecIssueLink(issueId, requirementId, linkType, effectiveCwd),
					)
					if (removed > 0) {
						yield* scheduleAutoPublishInternal("link_removed", effectiveCwd)
					}
					return removed
				}),
			listIssueRequirements: (issueId: string, cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* resolveEffectiveCwd(cwd)
					return yield* fromStore(
						"listIssueSpecRequirements",
						localIssueStore.listIssueSpecRequirements(issueId, effectiveCwd),
					)
				}),
			listRequirementIssues: (requirementId: string, cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* resolveEffectiveCwd(cwd)
					return yield* fromStore(
						"listRequirementLinkedIssues",
						localIssueStore.listRequirementLinkedIssues(requirementId, effectiveCwd),
					)
				}),
			getCoverageReport: (cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* resolveEffectiveCwd(cwd)
					return yield* fromStore(
						"getSpecCoverageReport",
						localIssueStore.getSpecCoverageReport(effectiveCwd),
					)
				}),
			getPublishConfig: (cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* resolveEffectiveCwd(cwd)
					return yield* fromStore(
						"getSpecPublishConfig",
						localIssueStore.getSpecPublishConfig(effectiveCwd),
					)
				}),
			setPublishConfig: (config: SpecPublishConfig, cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* resolveEffectiveCwd(cwd)
					yield* fromStore(
						"setSpecPublishConfig",
						localIssueStore.setSpecPublishConfig(config, effectiveCwd),
					)
				}),
			getLastPublishOutcome: (cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* resolveEffectiveCwd(cwd)
					return yield* fromStore(
						"getSpecPublishOutcome",
						localIssueStore.getSpecPublishOutcome(effectiveCwd),
					)
				}),
			publish: (cwd?: string) =>
				Effect.gen(function* () {
					return yield* runPublish(cwd)
				}),
			scheduleAutoPublish: (reason: string, cwd?: string) =>
				Effect.gen(function* () {
					yield* scheduleAutoPublishInternal(reason, cwd)
				}),
		} satisfies SpecServiceApi
	}),
}) {}
