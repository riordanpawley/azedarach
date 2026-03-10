const TOP_LEVEL_COMMAND_ALIASES: Readonly<Record<string, string>> = {
	a: "add",
	ls: "list",
	l: "list",
	at: "attach",
	pa: "pause",
	k: "kill",
	i: "issue",
	pr: "prime",
	g: "gate",
	sy: "sync",
	se: "status",
	p: "project",
	sp: "spec",
	n: "notify",
	h: "hooks",
	o: "opencode",
	d: "dev",
	s: "status",
	st: "start",
}

const TOP_LEVEL_NESTED_COMMAND_ALIASES: Readonly<Record<string, Readonly<Record<string, string>>>> =
	{
		issue: {
			l: "list",
			g: "get",
			c: "create",
			ch: "child",
			ck: "check",
			dr: "doctor",
			u: "update",
			d: "dep",
			x: "close",
			t: "close",
			rm: "delete",
			del: "delete",
		},
		"issue/dep": {
			a: "add",
		},
		spec: {
			r: "req",
			l: "link",
			p: "sync",
			c: "req",
			sy: "sync",
			publish: "publish",
		},
		"spec/req": {
			l: "list",
			g: "get",
			c: "create",
			u: "update",
			d: "delete",
			del: "delete",
			ls: "list",
			rm: "delete",
		},
		"spec/link": {
			l: "list",
			a: "add",
			r: "remove",
			rm: "remove",
		},
		"spec/publish": {
			r: "run",
			c: "config",
		},
		project: {
			a: "add",
			l: "list",
			r: "remove",
			rm: "remove",
			s: "switch",
			sw: "switch",
		},
		opencode: {
			i: "init",
			p: "plugin",
			pl: "plugin",
		},
		"opencode/plugin": {
			i: "install",
		},
		hooks: {
			i: "install",
			in: "install",
			ins: "install",
		},
		dev: {
			s: "start",
			st: "start",
			r: "restart",
			re: "restart",
			rs: "restart",
			x: "stop",
			stp: "stop",
			sto: "stop",
			ls: "list",
			l: "list",
			t: "status",
			stt: "status",
		},
	}

const TOP_LEVEL_SUBCOMMANDS = new Set([
	"add",
	"list",
	"i",
	"prime",
	"start",
	"attach",
	"pause",
	"kill",
	"status",
	"sync",
	"issue",
	"spec",
	"gate",
	"dev",
	"notify",
	"hooks",
	"project",
	"opencode",
])

export type CliExecutionMode = "tui" | "command" | "dev-command"

export const parseConfigPathFromArgv = (argv: ReadonlyArray<string>): string | null => {
	for (let index = 2; index < argv.length; index++) {
		const arg = argv[index]
		if (arg === "--") return null
		if (arg.startsWith("--config=")) {
			const value = arg.slice("--config=".length)
			return value.length > 0 ? value : null
		}
		if (arg.startsWith("-c=")) {
			const value = arg.slice("-c=".length)
			return value.length > 0 ? value : null
		}
		if ((arg === "--config" || arg === "-c") && index + 1 < argv.length) {
			const value = argv[index + 1]
			return value.length > 0 ? value : null
		}
	}
	return null
}

/**
 * @effect/cli expects options before positional args.
 * Normalize common user ordering like:
 *   az issue update <issue-id> --description "..."
 * into:
 *   az issue update --description "..." <issue-id>
 */
export const normalizeIssueOptionOrder = (argv: ReadonlyArray<string>): ReadonlyArray<string> => {
	const issueCommandIndex = argv.indexOf("issue")

	if (issueCommandIndex !== -1) {
		const subcommand = argv[issueCommandIndex + 1]
		if (subcommand === "dep") {
			const depSubcommand = argv[issueCommandIndex + 2]
			if (depSubcommand !== "add") {
				return argv
			}

			const issueIdIndex = issueCommandIndex + 3
			const dependsOnIdIndex = issueCommandIndex + 4
			if (dependsOnIdIndex >= argv.length) return argv

			const issueId = argv[issueIdIndex]
			const dependsOnId = argv[dependsOnIdIndex]
			if (
				issueId === undefined ||
				dependsOnId === undefined ||
				issueId.startsWith("-") ||
				dependsOnId.startsWith("-")
			) {
				return argv
			}

			const hasOptionAfterPositionalIds = argv
				.slice(dependsOnIdIndex + 1)
				.some((token) => token.startsWith("-"))
			if (!hasOptionAfterPositionalIds) return argv

			const reordered = [...argv]
			reordered.splice(issueIdIndex, 2)
			reordered.push(issueId, dependsOnId)
			return reordered
		}

		if (
			subcommand !== "get" &&
			subcommand !== "create" &&
			subcommand !== "child" &&
			subcommand !== "update" &&
			subcommand !== "close" &&
			subcommand !== "delete" &&
			subcommand !== "check" &&
			subcommand !== "doctor"
		) {
			return argv
		}

		const positionalArgIndex = issueCommandIndex + 2
		if (positionalArgIndex >= argv.length) return argv

		const positionalArg = argv[positionalArgIndex]
		if (positionalArg === undefined || positionalArg.startsWith("-")) {
			return argv
		}

		const hasOptionAfterPositional = argv
			.slice(positionalArgIndex + 1)
			.some((token) => token.startsWith("-"))
		if (!hasOptionAfterPositional) return argv

		const reordered = [...argv]
		reordered.splice(positionalArgIndex, 1)
		reordered.push(positionalArg)
		return reordered
	}

	return argv
}

// Backward-compatible name used by existing tests and imports.
export const normalizeIssueJsonFlagOrder = normalizeIssueOptionOrder

export const hasVerboseFlag = (argv: ReadonlyArray<string>): boolean =>
	argv.includes("--verbose") || argv.includes("-v")

const findTopLevelSubcommandIndex = (argv: ReadonlyArray<string>): number | null => {
	for (let index = 2; index < argv.length; index++) {
		const arg = argv[index]
		if (arg === "--") return null
		if (arg === "--config" || arg === "-c") {
			index += 1
			continue
		}
		if (arg.startsWith("--config=") || arg.startsWith("-c=") || arg.startsWith("-")) {
			continue
		}
		return index
	}
	return null
}

const normalizeTopLevelCommandAlias = (argv: ReadonlyArray<string>): ReadonlyArray<string> => {
	const topLevelIndex = findTopLevelSubcommandIndex(argv)
	if (topLevelIndex === null) return argv

	const topLevelArg = argv[topLevelIndex]
	if (topLevelArg === undefined) return argv

	const replacement = TOP_LEVEL_COMMAND_ALIASES[topLevelArg]
	if (replacement === undefined) return argv

	const normalized = [...argv]
	normalized[topLevelIndex] = replacement
	return normalized
}

export const normalizeCliAliases = (argv: ReadonlyArray<string>): ReadonlyArray<string> => {
	const withTopLevelAlias = normalizeTopLevelCommandAlias(argv)
	const topLevelIndex = findTopLevelSubcommandIndex(withTopLevelAlias)
	if (topLevelIndex === null) return withTopLevelAlias

	const originalTopLevelArg = argv[topLevelIndex]
	const topLevelArg = withTopLevelAlias[topLevelIndex]
	if (topLevelArg === undefined) return withTopLevelAlias

	const normalized = [...withTopLevelAlias]
	let commandPath = topLevelArg
	let currentIndex = topLevelIndex + 1

	// Keep the conventional `az ls` shorthand canonical when users pass a redundant
	// trailing `list` token (`az ls list` -> `az list`).
	if (
		(originalTopLevelArg === "ls" || originalTopLevelArg === "l") &&
		topLevelArg === "list" &&
		normalized[currentIndex] === "list"
	) {
		normalized.splice(currentIndex, 1)
	}

	while (currentIndex < normalized.length) {
		const candidate = normalized[currentIndex]
		if (candidate.startsWith("-")) {
			break
		}

		const aliasesForCommand = TOP_LEVEL_NESTED_COMMAND_ALIASES[commandPath]
		if (aliasesForCommand === undefined) {
			break
		}

		const replacement = aliasesForCommand[candidate]
		if (replacement === undefined) {
			break
		}

		normalized[currentIndex] = replacement
		commandPath = `${commandPath}/${replacement}`
		currentIndex += 1
	}

	return normalized
}

const parseTopLevelSubcommand = (argv: ReadonlyArray<string>): string | null => {
	const topLevelArgIndex = findTopLevelSubcommandIndex(argv)
	if (topLevelArgIndex === null) return null

	const arg = argv[topLevelArgIndex]
	return arg !== undefined && TOP_LEVEL_SUBCOMMANDS.has(arg) ? arg : null
}

const hasGlobalHelpOrVersionFlag = (argv: ReadonlyArray<string>): boolean =>
	argv.includes("--help") || argv.includes("-h") || argv.includes("--version")

export const resolveCliExecutionMode = (argv: ReadonlyArray<string>): CliExecutionMode => {
	const normalizedArgv = normalizeCliAliases(argv)
	const subcommand = parseTopLevelSubcommand(normalizedArgv)
	if (subcommand === null) {
		return hasGlobalHelpOrVersionFlag(normalizedArgv) ? "command" : "tui"
	}
	if (subcommand === "dev") {
		return "dev-command"
	}
	return "command"
}
