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
 * The `gt convoy list` command outputs:
 * ID       Name            Beads    Progress
 * hq-cv-1  Feature Auth    3        2/3
 */
const parseConvoyList = (output: string): ConvoyInfo[] => {
	const lines = output.trim().split("\n")
	// Skip header line
	const dataLines = lines.slice(1)

	return dataLines
		.map((line) => {
			const parts = line.trim().split(/\s+/)
			if (parts.length < 4) return null

			const [id, name, total, progress] = parts
			const [completed, totalStr] = progress.split("/").map(Number)

			return {
				id,
				name,
				beadIds: [], // Would need separate command to get full list
				completed: completed || 0,
				total: Number(totalStr) || Number(total) || 0,
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
				Effect.mapError((e) => new Error(`Gastown command failed: ${e}`)),
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
