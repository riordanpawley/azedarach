/**
 * VirtualList - A reusable virtualized list component for TUI
 *
 * Only renders `maxVisible` items at a time, keeping the selected item in view.
 * Uses the same windowing pattern as Column.tsx, extracted for reuse.
 *
 * @example
 * ```tsx
 * <VirtualList
 *   items={files}
 *   selectedIndex={selectedIndex}
 *   maxVisible={20}
 *   renderItem={(file, index, isSelected) => (
 *     <FileItem file={file} selected={isSelected} />
 *   )}
 * />
 * ```
 */

import { theme } from "../theme.js"

export interface VirtualListProps<T> {
	/** Array of items to render */
	items: readonly T[]
	/** Currently selected index */
	selectedIndex: number
	/** Maximum number of items to show at once */
	maxVisible?: number
	/** Render function for each item */
	renderItem: (item: T, index: number, isSelected: boolean) => React.ReactNode
	/** Unique key extractor for items */
	keyExtractor: (item: T, index: number) => string
	/** Show scroll position indicator (default: true) */
	showScrollIndicator?: boolean
	/** Color for scroll indicators */
	indicatorColor?: string
}

/** Default number of visible items */
const DEFAULT_MAX_VISIBLE = 20

/**
 * Calculate the visible window range given total items, selected index, and max visible.
 * Keeps the selected item in view, preferring to show it near the center when scrolling.
 */
export function calculateVisibleWindow(
	itemCount: number,
	selectedIndex: number,
	maxVisible: number,
): { startIndex: number; visibleCount: number; hiddenBefore: number; hiddenAfter: number } {
	const visibleCount = Math.min(maxVisible, itemCount)

	if (itemCount <= maxVisible) {
		return {
			startIndex: 0,
			visibleCount,
			hiddenBefore: 0,
			hiddenAfter: 0,
		}
	}

	// Scroll to keep selection visible, preferring near bottom when scrolling down
	let startIndex = 0
	if (selectedIndex >= maxVisible - 1) {
		startIndex = Math.min(selectedIndex - maxVisible + 2, itemCount - maxVisible)
	}
	startIndex = Math.max(0, startIndex)

	const hiddenBefore = startIndex
	const hiddenAfter = Math.max(0, itemCount - startIndex - visibleCount)

	return { startIndex, visibleCount, hiddenBefore, hiddenAfter }
}

export function VirtualList<T>({
	items,
	selectedIndex,
	maxVisible = DEFAULT_MAX_VISIBLE,
	renderItem,
	keyExtractor,
	showScrollIndicator = true,
	indicatorColor = theme.subtext0,
}: VirtualListProps<T>) {
	const itemCount = items.length
	const { startIndex, visibleCount, hiddenBefore, hiddenAfter } = calculateVisibleWindow(
		itemCount,
		selectedIndex,
		maxVisible,
	)

	// Get the visible slice of items
	const visibleItems = items.slice(startIndex, startIndex + visibleCount)

	return (
		<box flexDirection="column" flexGrow={1}>
			{/* Scroll indicator - top */}
			{showScrollIndicator && hiddenBefore > 0 && (
				<box paddingLeft={1}>
					<text fg={indicatorColor}>↑ {hiddenBefore} more</text>
				</box>
			)}

			{/* Item list */}
			<box flexDirection="column" flexGrow={1}>
				{visibleItems.map((item, localIndex) => {
					const actualIndex = startIndex + localIndex
					return (
						<box key={keyExtractor(item, actualIndex)}>
							{renderItem(item, actualIndex, actualIndex === selectedIndex)}
						</box>
					)
				})}
			</box>

			{/* Scroll indicator - bottom */}
			{showScrollIndicator && hiddenAfter > 0 && (
				<box paddingLeft={1}>
					<text fg={indicatorColor}>↓ {hiddenAfter} more</text>
				</box>
			)}
		</box>
	)
}

/**
 * VirtualListWithPosition - VirtualList variant that also shows position indicator
 *
 * Shows "{selected+1}/{total}" at the bottom when there are hidden items.
 */
export function VirtualListWithPosition<T>(
	props: VirtualListProps<T> & { positionColor?: string },
) {
	const {
		items,
		selectedIndex,
		maxVisible = DEFAULT_MAX_VISIBLE,
		positionColor = theme.subtext0,
	} = props
	const { hiddenBefore, hiddenAfter } = calculateVisibleWindow(
		items.length,
		selectedIndex,
		maxVisible,
	)
	const hasHidden = hiddenBefore > 0 || hiddenAfter > 0

	return (
		<box flexDirection="column" flexGrow={1}>
			<VirtualList {...props} showScrollIndicator={false} />

			{/* Combined scroll/position indicator */}
			{hasHidden && (
				<box paddingLeft={1}>
					<text fg={positionColor}>
						{hiddenBefore > 0 && <span>↑{hiddenBefore} </span>}
						<span>
							{selectedIndex + 1}/{items.length}
						</span>
						{hiddenAfter > 0 && <span> ↓{hiddenAfter}</span>}
					</text>
				</box>
			)}
		</box>
	)
}
