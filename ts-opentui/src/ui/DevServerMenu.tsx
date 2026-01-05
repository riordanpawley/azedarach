/**
 * DevServerMenu component - unified dev server management overlay
 *
 * Keybindings:
 * - j/k/↑/↓: Navigate server list
 * - ←/→/Space: Toggle start/stop for selected server
 * - Enter/a: Attach to server (enabled for running/starting/error states)
 * - Esc/q: Close overlay
 */

import { useAtomSet, useAtomValue } from "@effect-atom/atom-react"
import { useKeyboard } from "@opentui/react"
import { useState } from "react"
import type { DevServerView } from "./atoms.js"
import { attachDevServerAtom, beadDevServerViewsAtom, toggleDevServerAtom } from "./atoms.js"
import { useOverlays } from "./hooks/index.js"
import { theme } from "./theme.js"

interface Props {
	beadId: string
}

/** Status colors for dev server states */
const statusColor = (status: DevServerView["status"]) => {
	switch (status) {
		case "running":
			return theme.green
		case "starting":
			return theme.yellow
		case "error":
			return theme.red
		default:
			return theme.overlay0
	}
}

/** Check if a server can be attached to */
const canAttach = (status: DevServerView["status"]) =>
	status === "running" || status === "starting" || status === "error"

export const DevServerMenu = ({ beadId }: Props) => {
	const { dismiss } = useOverlays()
	const toggleDevServer = useAtomSet(toggleDevServerAtom, { mode: "promise" })
	const attachDevServer = useAtomSet(attachDevServerAtom, { mode: "promise" })

	const serverList = useAtomValue(beadDevServerViewsAtom(beadId))
	const [selectedIndex, setSelectedIndex] = useState(0)

	const selectedServer = serverList[selectedIndex]

	useKeyboard((event) => {
		switch (event.name) {
			// Navigation: j/k/up/down
			case "j":
			case "down":
				setSelectedIndex((i) => (i + 1) % serverList.length)
				break
			case "k":
			case "up":
				setSelectedIndex((i) => (i - 1 + serverList.length) % serverList.length)
				break

			// Toggle: left/right/space
			case "left":
			case "right":
			case "space":
				if (selectedServer) {
					toggleDevServer({ beadId, serverName: selectedServer.name })
				}
				break

			// Attach: enter/a (only for attachable states)
			case "return":
			case "a":
				if (selectedServer && canAttach(selectedServer.status)) {
					attachDevServer({ beadId, serverName: selectedServer.name })
				}
				break

			// Close: escape/q
			case "escape":
			case "q":
				dismiss()
				break
		}
	})

	return (
		<box
			borderStyle="single"
			border={true}
			borderColor={theme.mauve}
			backgroundColor={theme.base}
			paddingLeft={1}
			paddingRight={1}
			paddingTop={1}
			paddingBottom={1}
			flexDirection="column"
			position="absolute"
			left="25%"
			top="25%"
			width="50%"
		>
			<text fg={theme.mauve}>Dev Servers</text>

			<box flexDirection="column" marginTop={1}>
				{serverList.map((server: DevServerView, i: number) => {
					const isSelected = i === selectedIndex
					const attachable = canAttach(server.status)

					return (
						<box key={server.name} flexDirection="row" gap={1}>
							{/* Selection indicator */}
							<text fg={isSelected ? theme.lavender : theme.overlay0}>
								{isSelected ? "→" : " "}
							</text>

							{/* Server name */}
							<text fg={isSelected ? theme.text : theme.overlay0}>{server.name}</text>

							{/* Spacer */}
							<box flexGrow={1} />

							{/* Status */}
							<text fg={statusColor(server.status)}>{server.status}</text>

							{/* Port (if available) */}
							{server.port && <text fg={theme.overlay0}>:{server.port}</text>}

							{/* Attach indicator (dimmed if not attachable) */}
							<text fg={attachable ? theme.blue : theme.surface1}>
								{attachable ? "[a]" : "   "}
							</text>
						</box>
					)
				})}

				{serverList.length === 0 && <text fg={theme.overlay0}>No servers configured</text>}
			</box>

			{/* Keybindings help */}
			<box flexDirection="column" marginTop={1}>
				<text fg={theme.overlay0}>[j/k] Navigate [←/→/Space] Toggle</text>
				<text fg={theme.overlay0}>[Enter/a] Attach [Esc/q] Close</text>
			</box>
		</box>
	)
}
