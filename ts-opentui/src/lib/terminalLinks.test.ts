import { describe, expect, it } from "bun:test"
import {
	extractTerminalLinks,
	isAbsolutePath,
	isWindowsAbsolutePath,
	splitPathAndPosition,
} from "./terminalLinks.js"

describe("extractTerminalLinks", () => {
	it("extracts a plain HTTP URL", () => {
		const links = extractTerminalLinks("See https://example.com for details.")
		expect(links).toHaveLength(1)
		expect(links[0]).toMatchObject({ kind: "url", text: "https://example.com" })
	})

	it("trims trailing punctuation from URLs", () => {
		const links = extractTerminalLinks("Visit https://example.com.")
		expect(links[0]?.text).toBe("https://example.com")
	})

	it("trims unbalanced closing parenthesis from URLs", () => {
		const links = extractTerminalLinks("See (https://example.com/path) for info")
		expect(links[0]?.text).toBe("https://example.com/path")
	})

	it("extracts an absolute POSIX file path", () => {
		const links = extractTerminalLinks("Error in /home/user/project/src/main.ts")
		expect(links).toHaveLength(1)
		expect(links[0]).toMatchObject({ kind: "path", text: "/home/user/project/src/main.ts" })
	})

	it("extracts a relative path", () => {
		const links = extractTerminalLinks("Error in src/core/foo.ts:42")
		expect(links).toHaveLength(1)
		expect(links[0]).toMatchObject({ kind: "path", text: "src/core/foo.ts:42" })
	})

	it("extracts a tilde-home path", () => {
		const links = extractTerminalLinks("See ~/projects/app/index.ts for details")
		expect(links).toHaveLength(1)
		expect(links[0]).toMatchObject({ kind: "path", text: "~/projects/app/index.ts" })
	})

	it("does not duplicate URL matches as path matches", () => {
		const links = extractTerminalLinks("Visit https://example.com/path to continue")
		const urlLinks = links.filter((l) => l.kind === "url")
		const pathLinks = links.filter((l) => l.kind === "path")
		expect(urlLinks).toHaveLength(1)
		expect(pathLinks).toHaveLength(0)
	})

	it("returns results sorted by start position", () => {
		const links = extractTerminalLinks(
			"See /tmp/a.txt and https://example.com in the output",
		)
		expect(links[0]?.kind).toBe("path")
		expect(links[1]?.kind).toBe("url")
		expect((links[0]?.start ?? Infinity) < (links[1]?.start ?? 0)).toBe(true)
	})

	it("returns empty array for plain text with no links", () => {
		const links = extractTerminalLinks("No links here at all")
		expect(links).toHaveLength(0)
	})

	it("returns empty array for empty string", () => {
		expect(extractTerminalLinks("")).toHaveLength(0)
	})

	it("extracts multiple URLs from a single line", () => {
		const links = extractTerminalLinks(
			"Compare https://a.com with https://b.com",
		)
		const urls = links.filter((l) => l.kind === "url")
		expect(urls).toHaveLength(2)
	})
})

describe("splitPathAndPosition", () => {
	it("returns path only when no position suffix", () => {
		const result = splitPathAndPosition("src/foo.ts")
		expect(result).toEqual({ path: "src/foo.ts", line: undefined, column: undefined })
	})

	it("extracts line number from single :N suffix", () => {
		const result = splitPathAndPosition("src/foo.ts:10")
		expect(result).toEqual({ path: "src/foo.ts", line: "10", column: undefined })
	})

	it("extracts line and column from :L:C suffix", () => {
		const result = splitPathAndPosition("src/foo.ts:10:5")
		expect(result).toEqual({ path: "src/foo.ts", line: "10", column: "5" })
	})

	it("handles path with no extension", () => {
		const result = splitPathAndPosition("Makefile:42")
		expect(result).toEqual({ path: "Makefile", line: "42", column: undefined })
	})
})

describe("isWindowsAbsolutePath", () => {
	it("recognises Windows drive-letter paths", () => {
		expect(isWindowsAbsolutePath("C:\\Users\\user\\file.txt")).toBe(true)
		expect(isWindowsAbsolutePath("D:/projects/app")).toBe(true)
	})

	it("recognises UNC paths", () => {
		expect(isWindowsAbsolutePath("\\\\server\\share\\file")).toBe(true)
	})

	it("returns false for POSIX paths", () => {
		expect(isWindowsAbsolutePath("/home/user/file.txt")).toBe(false)
		expect(isWindowsAbsolutePath("relative/path")).toBe(false)
	})
})

describe("isAbsolutePath", () => {
	it("recognises POSIX absolute paths", () => {
		expect(isAbsolutePath("/home/user/file.txt")).toBe(true)
	})

	it("recognises Windows absolute paths", () => {
		expect(isAbsolutePath("C:\\Users\\file.txt")).toBe(true)
	})

	it("returns false for relative paths", () => {
		expect(isAbsolutePath("relative/path")).toBe(false)
		expect(isAbsolutePath("./local/path")).toBe(false)
		expect(isAbsolutePath("~/home/path")).toBe(false)
	})
})
