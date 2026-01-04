/**
 * TemplateService - Load and render session templates with variable substitution
 *
 * Supports simple Mustache-like syntax:
 * - {{VAR}} - Required variable substitution
 * - {{#VAR}}...{{/VAR}} - Conditional block (renders if VAR is truthy)
 *
 * Templates are loaded from .claude/session-templates/
 */

import { FileSystem, Path } from "@effect/platform"
import { Effect, Option } from "effect"

// ============================================================================
// Types
// ============================================================================

/**
 * Template variables for worker sessions
 */
export interface WorkerTemplateVariables {
	readonly TASK_ID: string
	readonly TASK_TITLE: string
	readonly TASK_DESCRIPTION?: string
	readonly TASK_DESIGN?: string
	readonly EPIC_ID: string
	readonly EPIC_TITLE: string
	readonly EPIC_DESIGN?: string
}

/**
 * Template rendering error
 */
export class TemplateError extends Error {
	readonly _tag = "TemplateError"
	constructor(
		readonly reason: string,
		readonly templateName?: string,
	) {
		super(`Template error${templateName ? ` (${templateName})` : ""}: ${reason}`)
	}
}

// ============================================================================
// Template Rendering
// ============================================================================

/**
 * Render a template string with variable substitution
 *
 * Supports:
 * - {{VAR}} - Simple substitution (empty string if undefined)
 * - {{#VAR}}content{{/VAR}} - Conditional block (renders if VAR is truthy)
 */
export const renderTemplate = (
	template: string,
	variables: Record<string, string | undefined>,
): string => {
	let result = template

	// Process conditional blocks first: {{#VAR}}...{{/VAR}}
	// This regex handles multiline content
	const conditionalRegex = /\{\{#(\w+)\}\}([\s\S]*?)\{\{\/\1\}\}/g
	result = result.replace(conditionalRegex, (_match, varName, content) => {
		const value = variables[varName]
		if (value && value.trim().length > 0) {
			// Render the content, also substituting the variable within
			return content.replace(new RegExp(`\\{\\{${varName}\\}\\}`, "g"), value)
		}
		return "" // Remove block if variable is empty/undefined
	})

	// Process simple substitutions: {{VAR}}
	const simpleRegex = /\{\{(\w+)\}\}/g
	result = result.replace(simpleRegex, (_match, varName) => {
		return variables[varName] ?? ""
	})

	// Clean up any double newlines from removed blocks
	result = result.replace(/\n{3,}/g, "\n\n")

	return result.trim()
}

// ============================================================================
// Service Definition
// ============================================================================

export class TemplateService extends Effect.Service<TemplateService>()("TemplateService", {
	effect: Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path

		/**
		 * Load a template file from .claude/session-templates/
		 */
		const loadTemplate = (templateName: string, projectPath: string) =>
			Effect.gen(function* () {
				const templatePath = pathService.join(
					projectPath,
					".claude",
					"session-templates",
					`${templateName}.md`,
				)

				const exists = yield* fs.exists(templatePath)
				if (!exists) {
					return yield* Effect.fail(
						new TemplateError(`Template not found: ${templatePath}`, templateName),
					)
				}

				const content = yield* fs
					.readFileString(templatePath)
					.pipe(
						Effect.mapError(
							(e) => new TemplateError(`Failed to read template: ${e.message}`, templateName),
						),
					)

				return content
			})

		/**
		 * Load and render the worker template with variables
		 */
		const renderWorkerTemplate = (variables: WorkerTemplateVariables, projectPath: string) =>
			Effect.gen(function* () {
				const template = yield* loadTemplate("worker", projectPath)

				const vars: Record<string, string | undefined> = {
					TASK_ID: variables.TASK_ID,
					TASK_TITLE: variables.TASK_TITLE,
					TASK_DESCRIPTION: variables.TASK_DESCRIPTION,
					TASK_DESIGN: variables.TASK_DESIGN,
					EPIC_ID: variables.EPIC_ID,
					EPIC_TITLE: variables.EPIC_TITLE,
					EPIC_DESIGN: variables.EPIC_DESIGN,
				}

				return renderTemplate(template, vars)
			})

		/**
		 * Try to load and render worker template, returning Option
		 * (graceful degradation if template doesn't exist)
		 */
		const tryRenderWorkerTemplate = (variables: WorkerTemplateVariables, projectPath: string) =>
			renderWorkerTemplate(variables, projectPath).pipe(
				Effect.map(Option.some),
				Effect.catchAll(() => Effect.succeed(Option.none())),
			)

		return {
			loadTemplate,
			renderTemplate: (template: string, variables: Record<string, string | undefined>) =>
				Effect.succeed(renderTemplate(template, variables)),
			renderWorkerTemplate,
			tryRenderWorkerTemplate,
		}
	}),
}) {}
