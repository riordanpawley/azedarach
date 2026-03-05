import { Console, Effect } from "effect"

const AZEDARACH_DIRECTORY = ".azedarach"
const GITIGNORE_FILENAME = ".gitignore"

const AZEDARACH_GITIGNORE_CONTENT = `# Azedarach local runtime state\n*\n!.gitignore\n`

interface GitignorePathService {
	readonly join: (...paths: readonly string[]) => string
}

interface GitignoreFileSystem {
	readonly exists: (path: string) => Effect.Effect<boolean, unknown>
	readonly makeDirectory: (
		path: string,
		options?: {
			readonly recursive?: boolean
		},
	) => Effect.Effect<void, unknown>
	readonly writeFileString: (path: string, data: string) => Effect.Effect<void, unknown>
}

interface EnsureProjectGitignoreInput {
	readonly projectPath: string
	readonly pathService: GitignorePathService
	readonly fs: GitignoreFileSystem
	readonly verbose: boolean
}

export const ensureProjectAzedarachGitignore = (input: EnsureProjectGitignoreInput) =>
	Effect.gen(function* () {
		const azedarachDir = input.pathService.join(input.projectPath, AZEDARACH_DIRECTORY)
		const gitignorePath = input.pathService.join(azedarachDir, GITIGNORE_FILENAME)

		const gitignoreExists = yield* input.fs.exists(gitignorePath)
		if (gitignoreExists) {
			if (input.verbose) {
				yield* Console.log(`Existing ${gitignorePath} found; leaving as-is.`)
			}
			return
		}

		const azedarachDirExists = yield* input.fs.exists(azedarachDir)
		if (!azedarachDirExists) {
			yield* input.fs.makeDirectory(azedarachDir, { recursive: true })
		}

		yield* input.fs.writeFileString(gitignorePath, AZEDARACH_GITIGNORE_CONTENT)

		if (input.verbose) {
			yield* Console.log(`Initialized ${gitignorePath}`)
		}
	})

export { AZEDARACH_GITIGNORE_CONTENT }
