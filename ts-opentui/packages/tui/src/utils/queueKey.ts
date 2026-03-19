export const buildTaskQueueKey = (taskId: string, projectPath?: string): string => {
	const normalizedProjectPath = projectPath?.trim()
	if (!normalizedProjectPath) {
		return taskId
	}
	return `${normalizedProjectPath}::${taskId}`
}
