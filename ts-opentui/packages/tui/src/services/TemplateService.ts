import { FileSystem, Path } from "@effect/platform"
import { Data, Effect, Option } from "effect"

export interface WorkerTemplateVariables {
	readonly TASK_ID: string
	readonly TASK_TITLE: string
	readonly TASK_DESCRIPTION?: string
	readonly TASK_DESIGN?: string
	readonly EPIC_ID: string
	readonly EPIC_TITLE: string
	readonly EPIC_DESIGN?: string
}

export class TemplateError extends Data.TaggedError("TemplateError")<{
	readonly reason: string
	readonly templateName?: string
}> {}

export const renderTemplate = (
	template: string,
	variables: Readonly<Record<string, string | undefined>>,
): string => {
	let result = template
	const conditionalRegex = /\{\{#(\w+)\}\}([\s\S]*?)\{\{\/\1\}\}/g
	result = result.replace(conditionalRegex, (_match, variableName: string, content: string) => {
		const value = variables[variableName]
		if (value !== undefined && value.trim().length > 0) {
			return content.replace(new RegExp(`\\{\\{${variableName}\\}\\}`, "g"), value)
		}
		return ""
	})

	const simpleRegex = /\{\{(\w+)\}\}/g
	result = result.replace(
		simpleRegex,
		(_match, variableName: string) => variables[variableName] ?? "",
	)
	result = result.replace(/\n{3,}/g, "\n\n")
	return result.trim()
}

export interface TemplateServiceApi {
	readonly loadTemplate: (
		templateName: string,
		projectPath: string,
	) => Effect.Effect<string, TemplateError>
	readonly renderTemplate: (
		template: string,
		variables: Readonly<Record<string, string | undefined>>,
	) => Effect.Effect<string, never>
	readonly renderWorkerTemplate: (
		variables: WorkerTemplateVariables,
		projectPath: string,
	) => Effect.Effect<string, TemplateError>
	readonly tryRenderWorkerTemplate: (
		variables: WorkerTemplateVariables,
		projectPath: string,
	) => Effect.Effect<Option.Option<string>, never>
}

export class TemplateService extends Effect.Service<TemplateService>()("TemplateService", {
	effect: Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path

		const loadTemplate: TemplateServiceApi["loadTemplate"] = (templateName, projectPath) =>
			Effect.gen(function* () {
				const templatePath = pathService.join(
					projectPath,
					".claude",
					"session-templates",
					`${templateName}.md`,
				)
				const exists = yield* fs.exists(templatePath).pipe(
					Effect.mapError(
						(error) =>
							new TemplateError({
								reason: `Failed to check template existence: ${error.message}`,
								templateName,
							}),
					),
				)
				if (!exists) {
					return yield* Effect.fail(
						new TemplateError({
							reason: `Template not found: ${templatePath}`,
							templateName,
						}),
					)
				}
				return yield* fs.readFileString(templatePath).pipe(
					Effect.mapError(
						(error) =>
							new TemplateError({
								reason: `Failed to read template: ${error.message}`,
								templateName,
							}),
					),
				)
			})

		const renderWorkerTemplate: TemplateServiceApi["renderWorkerTemplate"] = (
			variables,
			projectPath,
		) =>
			loadTemplate("worker", projectPath).pipe(
				Effect.map((template) =>
					renderTemplate(template, {
						TASK_ID: variables.TASK_ID,
						TASK_TITLE: variables.TASK_TITLE,
						TASK_DESCRIPTION: variables.TASK_DESCRIPTION,
						TASK_DESIGN: variables.TASK_DESIGN,
						EPIC_ID: variables.EPIC_ID,
						EPIC_TITLE: variables.EPIC_TITLE,
						EPIC_DESIGN: variables.EPIC_DESIGN,
					}),
				),
			)

		return {
			loadTemplate,
			renderTemplate: (template, variables) => Effect.succeed(renderTemplate(template, variables)),
			renderWorkerTemplate,
			tryRenderWorkerTemplate: (variables, projectPath) =>
				renderWorkerTemplate(variables, projectPath).pipe(
					Effect.map(Option.some),
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(Effect.succeed(Option.none<string>())),
						),
					),
				),
		} satisfies TemplateServiceApi
	}),
}) {}
