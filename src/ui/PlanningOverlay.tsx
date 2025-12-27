/**
 * PlanningOverlay - Modal for AI-powered task planning workflow
 *
 * A multi-step interface for:
 * 1. Entering a feature description
 * 2. Viewing plan generation progress
 * 3. Reviewing the generated plan with review feedback
 * 4. Confirming bead creation
 *
 * Uses the PlanningService for AI orchestration.
 */

import { Result } from "@effect-atom/atom"
import { useAtomValue, useSetAtom } from "@effect-atom/atom-react"
import { useKeyboard } from "@opentui/react"
import { useCallback, useState } from "react"
import type { Plan, PlanningState, ReviewFeedback } from "../core/PlanningService.js"
import { planningStateAtom, runPlanningAtom, resetPlanningAtom } from "./atoms/planning.js"
import { theme } from "./theme.js"

const ATTR_BOLD = 1

export interface PlanningOverlayProps {
	onClose: () => void
}

/**
 * Status indicator component
 */
const StatusIndicator = ({ status }: { status: PlanningState["status"] }) => {
	const colors: Record<PlanningState["status"], string> = {
		idle: theme.subtext0,
		generating: theme.yellow,
		reviewing: theme.blue,
		refining: theme.lavender,
		creating_beads: theme.green,
		complete: theme.green,
		error: theme.red,
	}

	const labels: Record<PlanningState["status"], string> = {
		idle: "Ready",
		generating: "Generating plan...",
		reviewing: "Reviewing plan...",
		refining: "Refining plan...",
		creating_beads: "Creating beads...",
		complete: "Complete!",
		error: "Error",
	}

	return (
		<box flexDirection="row">
			<text fg={colors[status]}>{"● "}</text>
			<text fg={colors[status]}>{labels[status]}</text>
		</box>
	)
}

/**
 * Progress bar for review passes
 */
const ReviewProgress = ({ current, max }: { current: number; max: number }) => {
	const filled = "█".repeat(current)
	const empty = "░".repeat(max - current)

	return (
		<box flexDirection="row">
			<text fg={theme.subtext0}>{"Review pass: "}</text>
			<text fg={theme.blue}>{filled}</text>
			<text fg={theme.surface1}>{empty}</text>
			<text fg={theme.subtext0}>{` ${current}/${max}`}</text>
		</box>
	)
}

/**
 * Task list view
 */
const TaskList = ({ plan }: { plan: Plan }) => (
	<box flexDirection="column" marginTop={1}>
		<text fg={theme.lavender} attributes={ATTR_BOLD}>
			{`Epic: ${plan.epicTitle}`}
		</text>
		<text fg={theme.subtext0} marginTop={1}>
			{plan.summary.slice(0, 100)}...
		</text>
		<text fg={theme.blue} marginTop={1}>
			{`${plan.tasks.length} tasks planned:`}
		</text>
		{plan.tasks.slice(0, 8).map((task, i) => (
			<box key={task.id} flexDirection="row" paddingLeft={2}>
				<text fg={task.canParallelize ? theme.green : theme.yellow}>
					{task.canParallelize ? "║" : "│"}
				</text>
				<text fg={theme.text}>{` ${task.title.slice(0, 50)}`}</text>
				{task.dependsOn.length > 0 && (
					<text fg={theme.subtext0}>{` (deps: ${task.dependsOn.join(", ")})`}</text>
				)}
			</box>
		))}
		{plan.tasks.length > 8 && (
			<text fg={theme.subtext0} paddingLeft={2}>
				{`  ... and ${plan.tasks.length - 8} more`}
			</text>
		)}
		{plan.parallelizationScore !== undefined && (
			<box flexDirection="row" marginTop={1}>
				<text fg={theme.subtext0}>{"Parallelization score: "}</text>
				<text
					fg={
						plan.parallelizationScore > 70
							? theme.green
							: plan.parallelizationScore > 40
								? theme.yellow
								: theme.red
					}
				>
					{`${plan.parallelizationScore}%`}
				</text>
			</box>
		)}
	</box>
)

/**
 * Review feedback summary
 */
const ReviewSummary = ({ feedback }: { feedback: ReviewFeedback }) => (
	<box flexDirection="column" marginTop={1}>
		<box flexDirection="row">
			<text fg={theme.subtext0}>{"Quality score: "}</text>
			<text
				fg={
					feedback.score > 80
						? theme.green
						: feedback.score > 50
							? theme.yellow
							: theme.red
				}
			>
				{`${feedback.score}/100`}
			</text>
			{feedback.isApproved && <text fg={theme.green}>{" (Approved)"}</text>}
		</box>
		{feedback.issues.length > 0 && (
			<box flexDirection="column" marginTop={1}>
				<text fg={theme.yellow}>{"Issues:"}</text>
				{feedback.issues.slice(0, 3).map((issue, i) => (
					<text key={i} fg={theme.subtext0} paddingLeft={2}>
						{`• ${issue.slice(0, 60)}`}
					</text>
				))}
			</box>
		)}
		{feedback.tasksTooLarge.length > 0 && (
			<text fg={theme.red} marginTop={1}>
				{`Tasks too large: ${feedback.tasksTooLarge.join(", ")}`}
			</text>
		)}
	</box>
)

/**
 * Input phase - enter feature description
 */
const InputPhase = ({
	description,
	setDescription,
	onSubmit,
	onCancel,
}: {
	description: string
	setDescription: (d: string) => void
	onSubmit: () => void
	onCancel: () => void
}) => {
	useKeyboard((event) => {
		if (event.name === "escape") {
			onCancel()
			return
		}

		if (event.name === "return" && description.trim()) {
			onSubmit()
			return
		}

		if (event.name === "backspace") {
			setDescription(description.slice(0, -1))
			return
		}

		if (event.ctrl && event.name === "u") {
			setDescription("")
			return
		}

		if (event.ctrl && event.name === "w") {
			setDescription(description.replace(/\S+\s*$/, ""))
			return
		}

		if (event.sequence && event.sequence.length >= 1 && !event.ctrl && !event.meta) {
			// biome-ignore lint/suspicious/noControlCharactersInRegex: Filtering control chars
			const printable = event.sequence.replace(/[\x00-\x1F\x7F]/g, "")
			if (printable) {
				setDescription(description + printable)
			}
		}
	})

	return (
		<box flexDirection="column">
			<text fg={theme.lavender} attributes={ATTR_BOLD}>
				{"Plan a New Feature"}
			</text>
			<text fg={theme.subtext0} marginTop={1}>
				{"Describe your feature. AI will create a plan with:"}
			</text>
			<text fg={theme.subtext0}>{"• Small, parallelizable tasks (30min-2hr each)"}</text>
			<text fg={theme.subtext0}>{"• Proper dependencies between tasks"}</text>
			<text fg={theme.subtext0}>{"• An epic to group related work"}</text>

			<box flexDirection="row" marginTop={2}>
				<text fg={theme.yellow}>{"❯ "}</text>
				<text fg={theme.text}>{description}</text>
				<text fg={theme.yellow}>{"_"}</text>
			</box>

			<text fg={theme.overlay0} marginTop={2}>
				{"Enter: generate plan  Esc: cancel  Ctrl-U: clear"}
			</text>
		</box>
	)
}

/**
 * Progress phase - show generation/review progress
 */
const ProgressPhase = ({
	state,
	onCancel,
}: {
	state: PlanningState
	onCancel: () => void
}) => {
	useKeyboard((event) => {
		if (event.name === "escape") {
			onCancel()
		}
	})

	const latestFeedback =
		state.reviewHistory.length > 0
			? state.reviewHistory[state.reviewHistory.length - 1]
			: null

	return (
		<box flexDirection="column">
			<text fg={theme.lavender} attributes={ATTR_BOLD}>
				{"Planning in Progress"}
			</text>

			<StatusIndicator status={state.status} />

			{state.status === "reviewing" || state.status === "refining" ? (
				<ReviewProgress current={state.reviewPass} max={state.maxReviewPasses} />
			) : null}

			{state.currentPlan && <TaskList plan={state.currentPlan} />}

			{latestFeedback && <ReviewSummary feedback={latestFeedback} />}

			<text fg={theme.overlay0} marginTop={2}>
				{"Esc: cancel"}
			</text>
		</box>
	)
}

/**
 * Complete phase - show results
 */
const CompletePhase = ({
	state,
	onClose,
	onReset,
}: {
	state: PlanningState
	onClose: () => void
	onReset: () => void
}) => {
	useKeyboard((event) => {
		if (event.name === "escape" || event.name === "return") {
			onClose()
		}
		if (event.name === "r") {
			onReset()
		}
	})

	return (
		<box flexDirection="column">
			<text fg={theme.green} attributes={ATTR_BOLD}>
				{"Planning Complete!"}
			</text>

			<text fg={theme.text} marginTop={1}>
				{`Created ${state.createdBeads.length} beads:`}
			</text>

			{state.createdBeads.slice(0, 10).map((bead) => (
				<box key={bead.id} flexDirection="row" paddingLeft={2}>
					<text fg={bead.issue_type === "epic" ? theme.lavender : theme.blue}>
						{`${bead.id}: `}
					</text>
					<text fg={theme.text}>{bead.title.slice(0, 50)}</text>
				</box>
			))}

			{state.createdBeads.length > 10 && (
				<text fg={theme.subtext0} paddingLeft={2}>
					{`... and ${state.createdBeads.length - 10} more`}
				</text>
			)}

			<text fg={theme.overlay0} marginTop={2}>
				{"Enter/Esc: close  r: plan another"}
			</text>
		</box>
	)
}

/**
 * Error phase - show error message
 */
const ErrorPhase = ({
	error,
	onClose,
	onRetry,
}: {
	error: string
	onClose: () => void
	onRetry: () => void
}) => {
	useKeyboard((event) => {
		if (event.name === "escape") {
			onClose()
		}
		if (event.name === "r") {
			onRetry()
		}
	})

	return (
		<box flexDirection="column">
			<text fg={theme.red} attributes={ATTR_BOLD}>
				{"Planning Failed"}
			</text>

			<text fg={theme.red} marginTop={1}>
				{error}
			</text>

			<text fg={theme.overlay0} marginTop={2}>
				{"r: retry  Esc: close"}
			</text>
		</box>
	)
}

/**
 * Main PlanningOverlay component
 */
export const PlanningOverlay = ({ onClose }: PlanningOverlayProps) => {
	const [description, setDescription] = useState("")
	const planningStateResult = useAtomValue(planningStateAtom)
	const runPlanning = useSetAtom(runPlanningAtom)
	const resetPlanning = useSetAtom(resetPlanningAtom)

	const state: PlanningState = Result.isSuccess(planningStateResult)
		? planningStateResult.value
		: {
				status: "idle",
				featureDescription: null,
				currentPlan: null,
				reviewPass: 0,
				maxReviewPasses: 5,
				reviewHistory: [],
				createdBeads: [],
				error: null,
			}

	const handleSubmit = useCallback(() => {
		if (description.trim()) {
			runPlanning(description.trim())
		}
	}, [description, runPlanning])

	const handleReset = useCallback(() => {
		resetPlanning()
		setDescription("")
	}, [resetPlanning])

	const handleClose = useCallback(() => {
		resetPlanning()
		onClose()
	}, [resetPlanning, onClose])

	const modalWidth = 80

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
				borderColor={theme.lavender}
				backgroundColor={theme.base}
				paddingLeft={2}
				paddingRight={2}
				paddingTop={1}
				paddingBottom={1}
				minWidth={modalWidth}
				maxHeight={30}
				flexDirection="column"
			>
				{state.status === "idle" && (
					<InputPhase
						description={description}
						setDescription={setDescription}
						onSubmit={handleSubmit}
						onCancel={handleClose}
					/>
				)}

				{(state.status === "generating" ||
					state.status === "reviewing" ||
					state.status === "refining" ||
					state.status === "creating_beads") && (
					<ProgressPhase state={state} onCancel={handleClose} />
				)}

				{state.status === "complete" && (
					<CompletePhase state={state} onClose={handleClose} onReset={handleReset} />
				)}

				{state.status === "error" && (
					<ErrorPhase
						error={state.error ?? "Unknown error"}
						onClose={handleClose}
						onRetry={handleReset}
					/>
				)}
			</box>
		</box>
	)
}
