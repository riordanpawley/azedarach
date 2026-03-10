import { describe, expect, it } from "bun:test"
import { OPENCODE_AZ_PLUGIN_SOURCE } from "./opencodePluginSource.js"

describe("OPENCODE_AZ_PLUGIN_SOURCE", () => {
	it("tells delegated agents to inherit orchestrator context and keep child issue tracking", () => {
		expect(OPENCODE_AZ_PLUGIN_SOURCE).toContain(
			"Subagents should inherit issue context from the orchestrator instead of rerunning",
		)
		expect(OPENCODE_AZ_PLUGIN_SOURCE).toContain("unless they explicitly need a fresh primer.")
		expect(OPENCODE_AZ_PLUGIN_SOURCE).not.toContain("Start delegated tasks by running")
		expect(OPENCODE_AZ_PLUGIN_SOURCE).toContain(
			"Create or claim a child issue under the active parent before substantive work",
		)
		expect(OPENCODE_AZ_PLUGIN_SOURCE).toContain('az issue child "Title"')
		expect(OPENCODE_AZ_PLUGIN_SOURCE).toContain("inherits the active parent context unless")
		expect(OPENCODE_AZ_PLUGIN_SOURCE).toContain("unless one is already assigned.")
		expect(OPENCODE_AZ_PLUGIN_SOURCE).toContain(
			"Keep child issue status and notes current while executing work.",
		)
	})
})
