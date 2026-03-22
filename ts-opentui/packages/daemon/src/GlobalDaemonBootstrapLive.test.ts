import { describe, expect, it } from "bun:test"
import { resolveGlobalDaemonSpawnCommand } from "./GlobalDaemonBootstrapLive.js"

describe("resolveGlobalDaemonSpawnCommand", () => {
	it("uses compiled daemon binary when running from compiled executable and az-daemon exists", () => {
		const result = resolveGlobalDaemonSpawnCommand({
			execPath: "/Users/riordan/prog/azedarach-te/ts-opentui/bin/az",
			bundledEntryPath: "/$bunfs/root/GlobalDaemonMain.ts",
			bunBinaryPath: "/opt/homebrew/bin/bun",
			hasCompiledDaemonBinary: true,
		})

		expect(result.command).toBe("./bin/az-daemon")
		expect(result.args).toEqual([])
	})

	it("uses bun binary and repo daemon path when running from compiled executable without az-daemon", () => {
		const result = resolveGlobalDaemonSpawnCommand({
			execPath: "/Users/riordan/prog/azedarach-te/ts-opentui/bin/az",
			bundledEntryPath: "/$bunfs/root/GlobalDaemonMain.ts",
			bunBinaryPath: "/opt/homebrew/bin/bun",
			hasCompiledDaemonBinary: false,
		})

		expect(result.command).toBe("/opt/homebrew/bin/bun")
		expect(result.args).toEqual(["run", "packages/daemon/src/GlobalDaemonMain.ts"])
	})

	it("uses current executable and bundled path when running under bun", () => {
		const result = resolveGlobalDaemonSpawnCommand({
			execPath: "/opt/homebrew/bin/bun",
			bundledEntryPath:
				"/Users/riordan/prog/azedarach-te/ts-opentui/packages/daemon/src/GlobalDaemonMain.ts",
			bunBinaryPath: "/opt/homebrew/bin/bun",
			hasCompiledDaemonBinary: true,
		})

		expect(result.command).toBe("/opt/homebrew/bin/bun")
		expect(result.args).toEqual([
			"run",
			"/Users/riordan/prog/azedarach-te/ts-opentui/packages/daemon/src/GlobalDaemonMain.ts",
		])
	})
})
