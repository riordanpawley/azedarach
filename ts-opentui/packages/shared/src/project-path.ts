import { FileSystem, Path } from "@effect/platform"
import { Effect, Option } from "effect"

export const resolveBaseProjectPath = (startPath: string) =>
	Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path
		let currentPath = pathService.normalize(startPath)
		while (true) {
			const gitEntryPath = pathService.join(currentPath, ".git")
			const hasGitEntry = yield* fs.exists(gitEntryPath).pipe(Effect.orElseSucceed(() => false))
			if (hasGitEntry) {
				const gitEntryContent = yield* fs.readFileString(gitEntryPath).pipe(Effect.option)
				if (Option.isSome(gitEntryContent)) {
					const trimmed = gitEntryContent.value.trim()
					const gitdirPrefix = "gitdir:"
					if (trimmed.startsWith(gitdirPrefix)) {
						const rawGitDir = trimmed.slice(gitdirPrefix.length).trim()
						const gitDirPath = rawGitDir.startsWith(pathService.sep)
							? rawGitDir
							: pathService.normalize(pathService.join(currentPath, rawGitDir))
						const worktreeMarker = `${pathService.sep}.git${pathService.sep}worktrees${pathService.sep}`
						const worktreeMarkerIndex = gitDirPath.indexOf(worktreeMarker)
						if (worktreeMarkerIndex > 0) {
							return gitDirPath.slice(0, worktreeMarkerIndex)
						}
					}
				}
				return currentPath
			}
			const parentPath = pathService.dirname(currentPath)
			if (parentPath === currentPath) {
				return pathService.normalize(startPath)
			}
			currentPath = parentPath
		}
	})

export const resolveEffectiveProjectPath = (projectPath: string | null | undefined): string =>
	projectPath ?? process.cwd()

type ProjectContextReader<R, E> = {
	readonly getCurrentPath: () => Effect.Effect<string | null, E, R>
}

export const resolveProjectPathFromContext = <R, E>(
	projectContext: ProjectContextReader<R, E>,
): Effect.Effect<string, E, R> =>
	projectContext.getCurrentPath().pipe(Effect.map(resolveEffectiveProjectPath))
