import path from "node:path"

export const getWorktreePath = (projectPath: string, issueId: string): string =>
	path.join(path.dirname(projectPath), `${path.basename(projectPath)}-${issueId}`)
