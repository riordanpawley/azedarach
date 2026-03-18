import { useAtomValue } from "@effect-atom/atom-react"
import { waitingSessionOptionsAtom } from "./atoms.js"
import { theme } from "./theme.js"

const ATTR_BOLD = 1
const MAX_VISIBLE_WAITING_SESSIONS = 9

export const WaitingSessionPicker = () => {
	const waitingSessions = useAtomValue(waitingSessionOptionsAtom)
	const visibleSessions = waitingSessions.slice(0, MAX_VISIBLE_WAITING_SESSIONS)

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
				borderColor={theme.yellow}
				backgroundColor={theme.base}
				paddingLeft={2}
				paddingRight={2}
				paddingTop={1}
				paddingBottom={1}
				minWidth={64}
				flexDirection="column"
			>
				<text fg={theme.yellow} attributes={ATTR_BOLD}>
					{"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"}
				</text>
				<text fg={theme.yellow} attributes={ATTR_BOLD}>
					{"  WAITING SESSIONS"}
				</text>
				<text fg={theme.yellow} attributes={ATTR_BOLD}>
					{"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"}
				</text>
				<text> </text>

				{visibleSessions.length === 0 ? (
					<box flexDirection="column">
						<text fg={theme.subtext0}>No waiting sessions found across tmux projects.</text>
					</box>
				) : (
					visibleSessions.map((session, index) => (
						<box key={`${session.sessionName}:${session.issueId}`} flexDirection="column">
							<box flexDirection="row">
								<text fg={theme.lavender}>{`  ${index + 1}. `}</text>
								<text
									fg={session.isRegisteredProject ? theme.text : theme.overlay1}
									attributes={session.isCurrentProject ? ATTR_BOLD : 0}
								>
									{`${session.projectName} / ${session.issueId}`}
								</text>
								{session.isCurrentProject && <text fg={theme.green}>{" (current)"}</text>}
								{!session.isRegisteredProject && <text fg={theme.peach}>{" (not added)"}</text>}
							</box>
							<text fg={theme.subtext0}>{`     ${session.sessionName}`}</text>
						</box>
					))
				)}

				<text> </text>
				<text fg={theme.subtext0}>
					{visibleSessions.length > 0
						? "Press 1-9 to switch, Escape to cancel"
						: "Press Escape to close"}
				</text>
				{waitingSessions.length > visibleSessions.length && (
					<text fg={theme.subtext0}>
						{`Showing first ${visibleSessions.length} of ${waitingSessions.length} waiting sessions`}
					</text>
				)}
			</box>
		</box>
	)
}
