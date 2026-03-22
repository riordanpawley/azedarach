import { describe, expect, it } from "bun:test"
import { resolveGlobalDaemonSpawnCommand } from "./GlobalDaemonBootstrapLive.js"

describe("resolveGlobalDaemonSpawnCommand", () => {
	it("uses bun binary and repo daemon path when running from compiled executable", () => {
		const result = resolveGlobalDaemonSpawnCommand({
			execPath: "/Users/riordan/prog/azedarach-te/ts-opentui/bin/az",
			bundledEntryPath: "/$bunfs/root/GlobalDaemonMain.ts",
			bunBinaryPath: "/opt/homebrew/bin/bun",
		})

		expect(result.bunExecutablePath).toBe("/opt/homebrew/bin/bun")
		expect(result.daemonMainEntryPath).toBe("packages/daemon/src/GlobalDaemonMain.ts")
	})

	it("uses current executable and bundled path when running under bun", () => {
		const result = resolveGlobalDaemonSpawnCommand({
			execPath: "/opt/homebrew/bin/bun",
			bundledEntryPath:
				"/Users/riordan/prog/azedarach-te/ts-opentui/packages/daemon/src/GlobalDaemonMain.ts",
			bunBinaryPath: "/opt/homebrew/bin/bun",
		})

		expect(result.bunExecutablePath).toBe("/opt/homebrew/bin/bun")
		expect(result.daemonMainEntryPath).toBe(
			"/Users/riordan/prog/azedarach-te/ts-opentui/packages/daemon/src/GlobalDaemonMain.ts",
		)
	})
})
