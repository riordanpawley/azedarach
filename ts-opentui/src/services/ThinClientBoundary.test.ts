import { describe, expect, it } from "bun:test"

const read = async (path: string): Promise<string> => await Bun.file(path).text()

const MIGRATED_THIN_CLIENT_FILES = [
	"src/ui/atoms/runtime.ts",
	"src/ui/atoms/task.ts",
	"src/ui/atoms/navigation.ts",
	"src/ui/atoms/spec.ts",
	"src/services/NavigationService.ts",
	"src/services/KeyboardService.ts",
	"src/services/keyboard/bindings.ts",
	"src/services/keyboard/TaskHandlersService.ts",
	"src/services/keyboard/OrchestrateHandlersService.ts",
] as const

const DAEMON_RUNTIME_REMOVED_AUTHORITY_SERVICES = [
    "SessionManager.Default",
    "PRWorkflow.Default",
    "IssueEditorService.Default",
] as const

describe("Thin client boundary (daemon rpc authority)", () => {
	it("keeps migrated ui/service paths off direct IssueTrackerClient authority", async () => {
		for (const path of MIGRATED_THIN_CLIENT_FILES) {
			const source = await read(path)
			expect(source).not.toMatch(/yield\*\s+IssueTrackerClient/)
			expect(source).not.toMatch(/IssueTrackerClient\.Default/)
			expect(source).not.toMatch(
				/issueTrackerClient\.(create|update|delete|addDependency|removeDependency)\s*\(/,
			)
		}
    })

    it("keeps daemon-overlap authority services out of daemon-rpc runtime composition", async () => {
        const source = await read("src/ui/atoms/runtime.ts")
        for (const serviceRegistration of DAEMON_RUNTIME_REMOVED_AUTHORITY_SERVICES) {
            expect(source).not.toContain(serviceRegistration)
        }

        expect(source).toContain("sessionManagerBoundaryLayer")
        expect(source).toContain("prWorkflowBoundaryLayer")
        expect(source).toContain("issueEditorBoundaryLayer")
    })
})
