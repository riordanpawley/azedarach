const getMessageFromUnknown = (value: unknown): string | undefined => {
	if (value instanceof Error) {
		return value.message
	}
	if (typeof value === "string") {
		const trimmed = value.trim()
		return trimmed.length > 0 ? trimmed : undefined
	}
	if (value && typeof value === "object") {
		const tagged = Reflect.get(value, "_tag")
		const message = Reflect.get(value, "message")
		if (typeof message === "string" && message.trim().length > 0) {
			if (typeof tagged === "string" && tagged.length > 0) {
				return `${tagged}: ${message.trim()}`
			}
			return message.trim()
		}
	}
	return undefined
}

export const formatForToast = (error: unknown): string => {
	return getMessageFromUnknown(error) ?? "Operation failed. Check logs for details."
}
