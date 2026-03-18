import { expect } from "bun:test"
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"

const decoder = new TextDecoder()
const ANSI_ESCAPE_PATTERN = String.raw`\u001b(?:\[[0-?]*[ -/]*[@-~]|[@-Z\\-_])`
const OSC_ESCAPE_PATTERN = String.raw`\u001b\][^\u0007]*(?:\u0007|\u001b\\)`
const CONTROL_CHARS_PATTERN = String.raw`[\u0000-\u0008\u000b-\u001f\u007f]`

const ANSI_ESCAPE = new RegExp(ANSI_ESCAPE_PATTERN, "g")
const OSC_ESCAPE = new RegExp(OSC_ESCAPE_PATTERN, "g")
const CONTROL_CHARS = new RegExp(CONTROL_CHARS_PATTERN, "g")

export const TS_OPENTUI_ROOT = join(import.meta.dir, "..")
export const AZ_BIN_PATH = join(TS_OPENTUI_ROOT, "bin", "az.ts")

const quote = (value: string): string => `'${value.replaceAll("'", "'\\''")}'`
const paneTarget = (sessionName: string): string => `${sessionName}:0.0`

const tmuxCmd = (args: readonly string[], socketName?: string): readonly string[] =>
	socketName === undefined ? ["tmux", ...args] : ["tmux", "-L", socketName, ...args]

const cleanPaneText = (value: string): string =>
	value
		.replace(OSC_ESCAPE, "")
		.replace(ANSI_ESCAPE, "")
		.replace(CONTROL_CHARS, "")
		.replace(/\r/g, "")

export const sleep = async (ms: number): Promise<void> => {
	await Bun.sleep(ms)
}

export const run = (cmd: readonly string[], cwd: string): { stdout: string; stderr: string } => {
	const result = Bun.spawnSync({
		cmd,
		cwd,
		stdout: "pipe",
		stderr: "pipe",
	})
	const stdout = decoder.decode(result.stdout).trim()
	const stderr = decoder.decode(result.stderr).trim()
	if (result.exitCode !== 0) {
		throw new Error(
			[
				`command failed with exit code ${result.exitCode}`,
				`cmd: ${cmd.join(" ")}`,
				`cwd: ${cwd}`,
				`stdout: ${stdout}`,
				`stderr: ${stderr}`,
			].join("\n"),
		)
	}
	return { stdout, stderr }
}

const runWithEnv = (
	cmd: readonly string[],
	cwd: string,
	env: Record<string, string>,
): { stdout: string; stderr: string } => {
	const result = Bun.spawnSync({
		cmd,
		cwd,
		env,
		stdout: "pipe",
		stderr: "pipe",
	})
	const stdout = decoder.decode(result.stdout).trim()
	const stderr = decoder.decode(result.stderr).trim()
	if (result.exitCode !== 0) {
		throw new Error(
			[
				`command failed with exit code ${result.exitCode}`,
				`cmd: ${cmd.join(" ")}`,
				`cwd: ${cwd}`,
				`stdout: ${stdout}`,
				`stderr: ${stderr}`,
			].join("\n"),
		)
	}
	return { stdout, stderr }
}

export const runAz = (args: readonly string[], cwd: string): string =>
	run(["bun", "run", AZ_BIN_PATH, ...args], cwd).stdout

export const runAzJson = <T>(args: readonly string[], cwd: string): T =>
	JSON.parse(runAz(args, cwd)) as T

export const tmuxAvailable = (): boolean => {
	const result = Bun.spawnSync({
		cmd: ["tmux", "-V"],
		stdout: "ignore",
		stderr: "ignore",
	})
	return result.exitCode === 0
}

export const createTempProject = async (): Promise<string> => {
	const dir = await mkdtemp(join(tmpdir(), "az-tui-e2e-"))
	const configDir = join(dir, ".azedarach")
	await mkdir(configDir, { recursive: true })
	await writeFile(
		join(configDir, "config.json"),
		`${JSON.stringify(
			{
				$schema: 4,
				issueTracker: {
					local: {
						syncEnabled: false,
					},
				},
			},
			null,
			2,
		)}\n`,
		"utf8",
	)
	return dir
}

export const removeTempProject = async (dir: string): Promise<void> => {
	await rm(dir, { recursive: true, force: true })
}

export const parseCreatedIssueId = (raw: string): string => {
	const match = raw.match(/Created issue\s+([A-Za-z0-9-]+)/)
	if (match === null || match[1] === undefined) {
		throw new Error(`Could not parse created issue id from output: ${raw}`)
	}
	return match[1]
}

export interface TuiSessionOptions {
	readonly tmuxSocketName?: string
	readonly env?: Record<string, string>
	readonly width?: number
	readonly height?: number
	readonly keepShellOpen?: boolean
}

export const startTuiSession = async (
	sessionName: string,
	projectDir: string,
	options?: TuiSessionOptions,
): Promise<void> => {
	const launch = [
		"cd",
		quote(projectDir),
		"&&",
		"AZ_NO_TMUX=1",
		"AZEDARACH_TUI_RUNTIME_MODE=daemon-rpc",
		"bun",
		"run",
		quote(AZ_BIN_PATH),
	].join(" ")
	const width = options?.width ?? 180
	const height = options?.height ?? 60
	const shell = process.env.SHELL ?? "/bin/zsh"
	const command =
		options?.keepShellOpen === true
			? `${shell} -i -c ${quote(`${launch}; exec ${shell} -i`)}`
			: launch
	const cmd = tmuxCmd(
		["new-session", "-d", "-s", sessionName, "-x", String(width), "-y", String(height), command],
		options?.tmuxSocketName,
	)
	if (options?.env === undefined) {
		run(cmd, TS_OPENTUI_ROOT)
		return
	}
	runWithEnv(cmd, TS_OPENTUI_ROOT, options.env)
}

export const killTuiSession = (sessionName: string, tmuxSocketName?: string): void => {
	const result = Bun.spawnSync({
		cmd: [...tmuxCmd(["kill-session", "-t", sessionName], tmuxSocketName)],
		stdout: "ignore",
		stderr: "ignore",
	})
	if (result.exitCode !== 0) return
}

export const sessionExists = (sessionName: string, tmuxSocketName?: string): boolean => {
	const result = Bun.spawnSync({
		cmd: [...tmuxCmd(["has-session", "-t", sessionName], tmuxSocketName)],
		stdout: "ignore",
		stderr: "ignore",
	})
	return result.exitCode === 0
}

export const capturePane = (sessionName: string, tmuxSocketName?: string, lines = 220): string =>
	run(
		[
			...tmuxCmd(
				["capture-pane", "-p", "-J", "-t", paneTarget(sessionName), "-S", `-${Math.abs(lines)}`],
				tmuxSocketName,
			),
		],
		TS_OPENTUI_ROOT,
	).stdout

export const sendKeys = (sessionName: string, ...keys: readonly string[]): void => {
	run([...tmuxCmd(["send-keys", "-t", paneTarget(sessionName), ...keys])], TS_OPENTUI_ROOT)
}

const sendKeysWithSocket = (
	sessionName: string,
	tmuxSocketName: string,
	...keys: readonly string[]
): void => {
	run(
		[...tmuxCmd(["send-keys", "-t", paneTarget(sessionName), ...keys], tmuxSocketName)],
		TS_OPENTUI_ROOT,
	)
}

export const waitForText = async (
	sessionName: string,
	text: string,
	timeoutMs = 20000,
	tmuxSocketName?: string,
): Promise<string> => {
	const deadline = Date.now() + timeoutMs
	let last = ""
	while (Date.now() < deadline) {
		last = capturePane(sessionName, tmuxSocketName)
		if (last.includes(text)) return last
		await sleep(150)
	}
	throw new Error(`Timed out waiting for text: ${text}\nLast pane:\n${last}`)
}

export const waitForTextGone = async (
	sessionName: string,
	text: string,
	timeoutMs = 10000,
	tmuxSocketName?: string,
): Promise<void> => {
	const deadline = Date.now() + timeoutMs
	let last = ""
	while (Date.now() < deadline) {
		last = capturePane(sessionName, tmuxSocketName)
		if (!last.includes(text)) return
		await sleep(150)
	}
	throw new Error(`Timed out waiting for text to disappear: ${text}\nLast pane:\n${last}`)
}

export const waitForCondition = async (
	check: () => boolean,
	timeoutMs: number,
	errorMessage: string,
): Promise<void> => {
	const deadline = Date.now() + timeoutMs
	while (Date.now() < deadline) {
		if (check()) return
		await sleep(200)
	}
	throw new Error(errorMessage)
}

export const quitTui = async (sessionName: string, tmuxSocketName?: string): Promise<void> => {
	if (!sessionExists(sessionName, tmuxSocketName)) return
	if (tmuxSocketName === undefined) {
		sendKeys(sessionName, "Escape")
		sendKeys(sessionName, "q")
	} else {
		sendKeysWithSocket(sessionName, tmuxSocketName, "Escape")
		sendKeysWithSocket(sessionName, tmuxSocketName, "q")
	}
	await waitForCondition(
		() => !sessionExists(sessionName, tmuxSocketName),
		5000,
		`Timed out waiting for tmux session ${sessionName} to exit after q`,
	).catch(async () => {
		if (tmuxSocketName === undefined) {
			sendKeys(sessionName, "C-c")
		} else {
			sendKeysWithSocket(sessionName, tmuxSocketName, "C-c")
		}
		await waitForCondition(
			() => !sessionExists(sessionName, tmuxSocketName),
			4000,
			`Timed out waiting for tmux session ${sessionName} to exit after C-c`,
		)
	})
}

export interface HarnessPaths {
	readonly root: string
	readonly home: string
	readonly xdgConfig: string
	readonly xdgState: string
	readonly xdgCache: string
	readonly tmp: string
	readonly artifacts: string
}

export interface TuiHarnessOptions {
	readonly projectDir: string
	readonly startupTimeoutMs?: number
}

export class TuiHarness {
	readonly paths: HarnessPaths
	readonly tmuxSocketName: string
	readonly sessionName: string
	private readonly options: TuiHarnessOptions
	private readonly env: Record<string, string>
	private started = false
	private lastPane = ""
	private lastRawPane = ""

	private constructor(options: TuiHarnessOptions, paths: HarnessPaths) {
		this.options = options
		this.paths = paths
		this.tmuxSocketName = `aze2e-${crypto.randomUUID().slice(0, 8)}`
		this.sessionName = `aze2e-${crypto.randomUUID().slice(0, 8)}`
		this.env = {
			...process.env,
			HOME: paths.home,
			XDG_CONFIG_HOME: paths.xdgConfig,
			XDG_STATE_HOME: paths.xdgState,
			XDG_CACHE_HOME: paths.xdgCache,
			TMPDIR: paths.tmp,
			TMUX_TMPDIR: "/tmp",
		}
	}

	static async create(options: TuiHarnessOptions): Promise<TuiHarness> {
		const root = await mkdtemp("/tmp/az-tui-e2e-run-")
		const paths: HarnessPaths = {
			root,
			home: join(root, "home"),
			xdgConfig: join(root, "xdg-config"),
			xdgState: join(root, "xdg-state"),
			xdgCache: join(root, "xdg-cache"),
			tmp: join(root, "tmp"),
			artifacts: join(root, "artifacts"),
		}
		await mkdir(paths.home, { recursive: true })
		await mkdir(paths.xdgConfig, { recursive: true })
		await mkdir(paths.xdgState, { recursive: true })
		await mkdir(paths.xdgCache, { recursive: true })
		await mkdir(paths.tmp, { recursive: true })
		await mkdir(paths.artifacts, { recursive: true })
		return new TuiHarness(options, paths)
	}

	async start(): Promise<void> {
		await startTuiSession(this.sessionName, this.options.projectDir, {
			tmuxSocketName: this.tmuxSocketName,
			env: this.env,
			keepShellOpen: true,
		})
		this.started = true
	}

	isStarted(): boolean {
		return this.started
	}

	send(keys: string | readonly string[]): void {
		const values = typeof keys === "string" ? [keys] : [...keys]
		runWithEnv(
			[
				...tmuxCmd(
					["send-keys", "-t", paneTarget(this.sessionName), ...values],
					this.tmuxSocketName,
				),
			],
			TS_OPENTUI_ROOT,
			this.env,
		)
	}

	capturePane(lines = 220): string {
		try {
			const pane = cleanPaneText(
				runWithEnv(
					[
						...tmuxCmd(
							["capture-pane", "-p", "-J", "-t", paneTarget(this.sessionName), "-S", `-${lines}`],
							this.tmuxSocketName,
						),
					],
					TS_OPENTUI_ROOT,
					this.env,
				).stdout,
			)
			this.lastPane = pane
			return pane
		} catch {
			return this.lastPane
		}
	}

	private captureRawPane(lines = 2200): string {
		try {
			const pane = runWithEnv(
				[
					...tmuxCmd(
						[
							"capture-pane",
							"-p",
							"-e",
							"-J",
							"-t",
							paneTarget(this.sessionName),
							"-S",
							`-${lines}`,
						],
						this.tmuxSocketName,
					),
				],
				TS_OPENTUI_ROOT,
				this.env,
			).stdout
			this.lastRawPane = pane
			return pane
		} catch {
			return this.lastRawPane
		}
	}

	async waitForText(text: string, timeoutMs?: number): Promise<string> {
		const deadline = Date.now() + (timeoutMs ?? this.options.startupTimeoutMs ?? 20000)
		let last = ""
		while (Date.now() < deadline) {
			last = this.capturePane(2200)
			if (last.includes(text)) {
				return last
			}
			await sleep(150)
		}
		throw new Error(`Timed out waiting for text: ${text}\nLast pane:\n${last}`)
	}

	assertContains(text: string): void {
		expect(this.capturePane()).toContain(text)
	}

	assertNoInterruptedExceptionSpam(maxOccurrences = 1): void {
		const pane = this.capturePane(2200)
		const occurrences = pane.match(/InterruptedException/g)?.length ?? 0
		expect(
			occurrences,
			`Detected InterruptedException spam (${occurrences} occurrences):\n${pane}`,
		).toBeLessThanOrEqual(maxOccurrences)
	}

	private paneCurrentCommand(): string {
		const result = runWithEnv(
			[
				...tmuxCmd(
					["display-message", "-p", "-t", paneTarget(this.sessionName), "#{pane_current_command}"],
					this.tmuxSocketName,
				),
			],
			TS_OPENTUI_ROOT,
			this.env,
		)
		return result.stdout.trim()
	}

	private sessionExists(): boolean {
		const result = Bun.spawnSync({
			cmd: [...tmuxCmd(["has-session", "-t", this.sessionName], this.tmuxSocketName)],
			cwd: TS_OPENTUI_ROOT,
			env: this.env,
			stdout: "ignore",
			stderr: "ignore",
		})
		return result.exitCode === 0
	}

	async quit(): Promise<void> {
		this.send(["Escape", "q"])
		await waitForCondition(
			() => !this.sessionExists() || this.paneCurrentCommand() !== "bun",
			10000,
			`Timed out waiting for ${this.sessionName} to quit cleanly`,
		).catch(async () => {
			this.send("C-c")
		})
		if (this.sessionExists()) {
			this.send(["exit", "Enter"])
			await waitForCondition(
				() => !this.sessionExists(),
				2000,
				`Timed out waiting for ${this.sessionName} shell to exit`,
			).catch(() => {
				killTuiSession(this.sessionName, this.tmuxSocketName)
			})
		}
	}

	async writeArtifacts(
		prefix = "startup-smoke",
	): Promise<{ readonly pane: string; readonly rawPane: string }> {
		const panePath = join(this.paths.artifacts, `${prefix}.pane.txt`)
		const rawPanePath = join(this.paths.artifacts, `${prefix}.pane.raw.txt`)
		await writeFile(panePath, `${this.capturePane(2200)}\n`, "utf8")
		await writeFile(rawPanePath, `${this.captureRawPane(2200)}\n`, "utf8")
		return { pane: panePath, rawPane: rawPanePath }
	}

	async cleanup(removeRunDir = true): Promise<void> {
		if (this.started) {
			await this.quit().catch(() => undefined)
			killTuiSession(this.sessionName, this.tmuxSocketName)
		}
		this.started = false
		if (removeRunDir) {
			await rm(this.paths.root, { recursive: true, force: true })
		}
	}
}
