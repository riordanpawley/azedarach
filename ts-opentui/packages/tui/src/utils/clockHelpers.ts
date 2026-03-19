import { DateTime } from "effect"

const formatElapsedMs = (elapsedMs: number): string => {
	const totalSeconds = Math.max(0, Math.floor(elapsedMs / 1000))

	const days = Math.floor(totalSeconds / 86400)
	const hours = Math.floor((totalSeconds % 86400) / 3600)
	const minutes = Math.floor((totalSeconds % 3600) / 60)
	const seconds = totalSeconds % 60

	if (days > 0) {
		return hours > 0 ? `${days}d ${hours}h` : `${days}d`
	}
	if (hours > 0) {
		return minutes > 0 ? `${hours}h ${minutes}m` : `${hours}h`
	}
	if (minutes > 0) {
		return seconds > 0 ? `${minutes}m ${seconds}s` : `${minutes}m`
	}
	return `${seconds}s`
}

const computeElapsedMs = (startedAt: string, now: DateTime.Utc): number => {
	const start = DateTime.unsafeMake(startedAt)
	return DateTime.distance(start, now)
}

export const computeElapsedFormatted = (startedAt: string, now: DateTime.Utc): string =>
	formatElapsedMs(computeElapsedMs(startedAt, now))
