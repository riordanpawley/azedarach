/**
 * DevServerMenu component - lets user pick which dev server to toggle
 */

import { useAtomSet, useAtomValue } from "@effect-atom/atom-react"
import { useKeyboard } from "@opentui/react"
import React, { useState } from "react"
import type { DevServerView } from "./atoms.js"
import { beadDevServerViewsAtom, toggleDevServerAtom } from "./atoms.js"
import { useOverlays } from "./hooks/index.js"
import { theme } from "./theme.js"

interface Props {
	beadId: string
	mode: "toggle" | "attach"
}

export const DevServerMenu = ({ beadId, mode }: Props) => {
	const { dismiss } = useOverlays()
	const toggleDevServer = useAtomSet(toggleDevServerAtom, { mode: "promise" })

	const allViews = useAtomValue(beadDevServerViewsAtom(beadId))

	const serverList =
		mode === "attach"
			? allViews.filter((v: DevServerView) => v.status === "running" || v.status === "starting")
			: allViews

	const [selectedIndex, setSelectedIndex] = useState(0)

	useKeyboard((event) => {
		switch (event.name) {
			case "j":
			case "down":
				setSelectedIndex((i) => (i + 1) % serverList.length)
				break
			case "k":
			case "up":
				setSelectedIndex((i) => (i - 1 + serverList.length) % serverList.length)
				break
			case "return":
			case "space": {
				const selected = serverList[selectedIndex]
				if (selected) {
					toggleDevServer({ beadId, serverName: selected.name })
					dismiss()
				}
				break
			}
			case "escape":
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
			<text fg={theme.mauve}>
				{mode === "attach" ? "Attach to Dev Server" : "Toggle Dev Server"}
			</text>
			<box flexDirection="column" marginTop={1}>
				{serverList.map((server: DevServerView, i: number) => (
					<box key={server.name} flexDirection="row" gap={1}>
						<text fg={i === selectedIndex ? theme.lavender : theme.overlay0}>
							{i === selectedIndex ? "→" : " "}
						</text>
						<text fg={i === selectedIndex ? theme.text : theme.overlay0}>{server.name}</text>
						<box flexGrow={1} />
						<text
							fg={
								server.status === "running"
									? theme.green
									: server.status === "starting"
										? theme.yellow
										: server.status === "error"
											? theme.red
											: theme.overlay0
							}
						>
							{server.status}
						</text>
						{server.port && <text fg={theme.overlay0}>:{server.port}</text>}
					</box>
				))}
				{serverList.length === 0 && <text fg={theme.overlay0}>No servers available</text>}
			</box>
			<box marginTop={1}>
				<text fg={theme.overlay0}>[j/k] Navigate, [Enter/Space] Select, [Esc] Cancel</text>
			</box>
		</box>
	)
}
