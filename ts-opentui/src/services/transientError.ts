const TRANSIENT_ERROR_MESSAGE_PATTERNS: ReadonlyArray<string> = [
	"database is locked",
	"sqlite_busy",
	"sqlite busy",
	"temporarily unavailable",
	"timed out",
	"timeout",
	"resource busy",
	"connection reset",
	"broken pipe",
]

const TRANSIENT_ERROR_FIELDS: ReadonlyArray<"message" | "stderr" | "stdout"> = [
	"message",
	"stderr",
	"stdout",
]

const getErrorField = (
	error: object,
	fieldName: "message" | "stderr" | "stdout",
): string | undefined => {
	const value = Reflect.get(error, fieldName)
	return typeof value === "string" ? value : undefined
}

export const isTransientOperationalErrorMessage = (value: string): boolean => {
	const normalized = value.toLowerCase()
	return TRANSIENT_ERROR_MESSAGE_PATTERNS.some((pattern) => normalized.includes(pattern))
}

export const isTransientOperationalError = (error: unknown): boolean => {
	if (typeof error === "string") {
		return isTransientOperationalErrorMessage(error)
	}

	if (error instanceof Error) {
		return isTransientOperationalErrorMessage(error.message)
	}

	if (typeof error === "object" && error !== null) {
		return TRANSIENT_ERROR_FIELDS.some((fieldName) => {
			const value = getErrorField(error, fieldName)
			return value !== undefined && isTransientOperationalErrorMessage(value)
		})
	}

	return isTransientOperationalErrorMessage(String(error))
}
