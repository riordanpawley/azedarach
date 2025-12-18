/**
 * Pure path utility functions that don't require Path service
 */

/**
 * Generate tmux session name for a bead
 */
export function getSessionName(beadId: string): string {
	return beadId // Use bead ID directly as session name
}
