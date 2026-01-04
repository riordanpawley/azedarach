/**
 * ProjectSelector component - modal for switching between projects
 *
 * Displays a list of registered projects with the current project highlighted.
 * Press number key or Enter to select, Escape to dismiss.
 */
import { Result } from "@effect-atom/atom"
import { useAtomValue } from "@effect-atom/atom-react"
import type { Project } from "../services/ProjectService.js"
import { currentProjectAtom, projectsAtom } from "./atoms.js"
import { theme } from "./theme.js"

const ATTR_BOLD = 1

/**
 * ProjectSelector overlay component
 *
 * Shows a list of registered projects. User can:
 * - Press number keys (1-9) to select a project
 * - Press Enter to select the highlighted project
 * - Press Escape to dismiss without changing
 */
export const ProjectSelector = () => {
	const projectsResult = useAtomValue(projectsAtom)
	const currentResult = useAtomValue(currentProjectAtom)

	// Extract project list with type-safe default
	const emptyProjects: ReadonlyArray<Project> = []
	const projects = Result.isSuccess(projectsResult) ? projectsResult.value : emptyProjects

	const current = Result.isSuccess(currentResult) ? currentResult.value : undefined

	return (
		<box
			position="absolute"
			left={0}
			right={0}
			top={0}
			bottom={0}
			alignItems="center"
			justifyContent="center"
			backgroundColor={`${theme.crust}CC`}
		>
			<box
				borderStyle="rounded"
				border={true}
				borderColor={theme.blue}
				backgroundColor={theme.base}
				paddingLeft={2}
				paddingRight={2}
				paddingTop={1}
				paddingBottom={1}
				minWidth={50}
				flexDirection="column"
			>
				{/* Header */}
				<text fg={theme.blue} attributes={ATTR_BOLD}>
					{"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"}
				</text>
				<text fg={theme.blue} attributes={ATTR_BOLD}>
					{"  PROJECT SELECTOR"}
				</text>
				<text fg={theme.blue} attributes={ATTR_BOLD}>
					{"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"}
				</text>
				<text> </text>

				{/* Project list */}
				{projects.length === 0 ? (
					<box flexDirection="column">
						<text fg={theme.subtext0}>No projects registered.</text>
						<text> </text>
						<text fg={theme.subtext0}>Use CLI to add projects:</text>
						<text fg={theme.lavender}>{"  az project add /path/to/project"}</text>
					</box>
				) : (
					projects.map((project, index) => {
						const isSelected = project.name === current?.name
						const number = index + 1
						return (
							<box key={project.name} flexDirection="row">
								<text fg={theme.lavender}>{`  ${number}. `}</text>
								<text
									fg={isSelected ? theme.green : theme.text}
									attributes={isSelected ? ATTR_BOLD : 0}
								>
									{project.name}
								</text>
								{isSelected && <text fg={theme.green}>{" (current)"}</text>}
							</box>
						)
					})
				)}

				<text> </text>

				{/* Footer */}
				<text fg={theme.subtext0}>
					{projects.length > 0 ? "Press 1-9 to select, Escape to cancel" : "Press Escape to close"}
				</text>
			</box>
		</box>
	)
}
