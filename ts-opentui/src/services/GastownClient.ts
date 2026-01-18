/**
 * Gastown CLI Client Service
 *
 * Wraps the `gt` CLI for Gastown operations when running in Gastown mode.
 * Provides structured interfaces for:
 * - Convoy management (create, list, show, add)
 * - Agent operations (sling, list agents)
 * - Configuration (get/set default agent)
 */

import { Command, CommandExecutor } from "@effect/platform"
import { Effect, Schema } from "effect"
import type { GastownAgentRuntime } from "../config/schema.js"

/**
 * Convoy information
 */
export interface ConvoyInfo {
	id: string
	name: string
	beadIds: string[]
	completed: number
	total: number
}

/**
 * Agent session information
 */
export interface AgentSessionInfo {
	beadId: string
	rig: string
	agent: string
	status: "running" | "waiting" | "done" | "error"
}

/**
 * Parse convoy list output
 *
 * The `gt convoy list` command outputs a table format:
 * Header: ID       Name            Beads    Progress
 * Data:   hq-cv-1  Feature Auth    3        2/3
 *
 * Columns:
 * - ID: Convoy identifier
 * - Name: Convoy display name (may contain spaces)
 * - Beads: Total number of beads in convoy
 * - Progress: Completed/Total format (e.g., "2/3")
 *
 * Parses the data rows and returns structured convoy information.
 * Returns empty array if output is malformed or empty.
 */
const parseConvoyList = (output: string): ConvoyInfo[] => {
	const lines = output.trim().split("\n")
	if (lines.length < 2) {
		// No data rows (only header or empty)
		return []
	}

	// Skip header line, process data rows
	const dataLines = lines.slice(1)

	// Expected format: at least ID, Name, and Progress columns
	const MIN_COLUMNS = 3

	return dataLines
		.map((line) => {
			// Split on whitespace - this may break names with spaces
			// TODO: Consider using fixed-width column parsing for robustness
			const parts = line.trim().split(/\s+/)
			if (parts.length < MIN_COLUMNS) {
				// Malformed line, skip it
				return null
			}

			const id = parts[0]
			// Last part is progress (e.g., "2/3")
			const progressStr = parts[parts.length - 1]
			// Second-to-last is bead count (may be redundant with progress total)
			const beadCount = parts[parts.length - 2]
			// Everything in between is the name (may have spaces)
			const name = parts.slice(1, parts.length - 2).join(" ")

			// Parse progress string (e.g., "2/3" -> completed: 2, total: 3)
			const progressMatch = progressStr.match(/^(\d+)\/(\d+)$/)
			if (!progressMatch) {
				// Invalid progress format, use bead count as total
				return {
					id,
					name,
					beadIds: [],
					completed: 0,
					total: Number(beadCount) || 0,
				}
			}

			const [, completedStr, totalStr] = progressMatch
			return {
				id,
				name,
				beadIds: [], // Would need separate command to get full list
				completed: Number(completedStr) || 0,
				total: Number(totalStr) || 0,
			}
		})
		.filter((item): item is ConvoyInfo => item !== null)
}

/**
 * Gastown CLI Client Service
 *
 * Provides Effect-based wrappers around the `gt` CLI commands.
 * All commands return structured data via Effect, with proper error handling.
 */
export class GastownClient extends Effect.Service<GastownClient>()("GastownClient", {
	dependencies: [CommandExecutor.CommandExecutor.Default],
	effect: Effect.gen(function* () {
		const executor = yield* CommandExecutor.CommandExecutor

		/**
		 * Execute a gt command and return stdout
		 */
		const runGtCommand = (args: string[]): Effect.Effect<string, Error> =>
			Command.make("gt", ...args).pipe(
				Command.string,
				Effect.mapError(
					(e) =>
						new Error(
							`Gastown command 'gt ${args.join(" ")}' failed: ${e.message || String(e)}`,
						),
				),
			)

		return {
			/**
			 * Create a new convoy with beads
			 *
			 * @param name - Convoy name
			 * @param beadIds - List of bead IDs to include
			 * @param options - Additional options
			 * @returns Effect that creates the convoy
			 */
			convoyCreate: (
				name: string,
				beadIds: string[],
				options?: { notify?: boolean; human?: boolean },
			): Effect.Effect<string, Error> => {
				const args = ["convoy", "create", name, ...beadIds]
				if (options?.notify) args.push("--notify")
				if (options?.human) args.push("--human")
				return runGtCommand(args)
			},

			/**
			 * List all convoys
			 *
			 * @returns Effect with array of convoy information
			 */
			convoyList: (): Effect.Effect<ConvoyInfo[], Error> =>
				runGtCommand(["convoy", "list"]).pipe(Effect.map(parseConvoyList)),

			/**
			 * Show details of a specific convoy
			 *
			 * @param convoyId - Convoy ID
			 * @returns Effect with convoy details
			 */
			convoyShow: (convoyId?: string): Effect.Effect<string, Error> => {
				const args = ["convoy", "show"]
				if (convoyId) args.push(convoyId)
				return runGtCommand(args)
			},

			/**
			 * Add beads to an existing convoy
			 *
			 * @param convoyId - Convoy ID
			 * @param beadIds - Bead IDs to add
			 * @returns Effect that adds the beads
			 */
			convoyAdd: (convoyId: string, beadIds: string[]): Effect.Effect<string, Error> =>
				runGtCommand(["convoy", "add", convoyId, ...beadIds]),

			/**
			 * Sling a bead to a rig (assign work to an agent)
			 *
			 * @param beadId - Bead ID to assign
			 * @param rig - Rig name (project)
			 * @param options - Additional options (agent runtime override, etc.)
			 * @returns Effect that slings the bead
			 */
			sling: (
				beadId: string,
				rig: string,
				options?: { agent?: GastownAgentRuntime },
			): Effect.Effect<string, Error> => {
				const args = ["sling", beadId, rig]
				if (options?.agent) {
					args.push("--agent", options.agent)
				}
				return runGtCommand(args)
			},

			/**
			 * List active agents
			 *
			 * @returns Effect with agent list output
			 */
			agents: (): Effect.Effect<string, Error> => runGtCommand(["agents"]),

			/**
			 * Get the default agent runtime
			 *
			 * @returns Effect with default agent name
			 */
			getDefaultAgent: (): Effect.Effect<string, Error> =>
				runGtCommand(["config", "get", "default-agent"]).pipe(Effect.map((s) => s.trim())),

			/**
			 * List available agent presets
			 *
			 * @returns Effect with agent list output
			 */
			listAgents: (): Effect.Effect<string, Error> => runGtCommand(["config", "agent", "list"]),

			/**
			 * Set the default agent runtime
			 *
			 * @param agent - Agent runtime to set as default
			 * @returns Effect that sets the default
			 */
			setDefaultAgent: (agent: GastownAgentRuntime): Effect.Effect<string, Error> =>
				runGtCommand(["config", "set", "default-agent", agent]),

			/**
			 * Prime a session (context recovery for existing sessions)
			 *
			 * @returns Effect that primes the current session
			 */
			prime: (): Effect.Effect<string, Error> => runGtCommand(["prime"]),

			/**
			 * Check mail and optionally inject into session
			 *
			 * @param options - Mail check options
			 * @returns Effect that checks mail
			 */
			mailCheck: (options?: { inject?: boolean }): Effect.Effect<string, Error> => {
				const args = ["mail", "check"]
				if (options?.inject) args.push("--inject")
				return runGtCommand(args)
			},
		}
	}),
}) {}
