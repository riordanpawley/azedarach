package board

const (
	BoardStatusBarLines = 1
	ColumnHeaderLines   = 2
	ColumnContentInsetX = 2
	DefaultColumnCount  = 4
	MinVisibleColumns   = 2
	MinColumnWidth      = 30
)

func BoardContentHeight(totalHeight int) int {
	h := totalHeight - BoardStatusBarLines
	if h < 0 {
		return 0
	}
	return h
}

func ColumnBodyHeight(boardContentHeight int) int {
	h := boardContentHeight - ColumnHeaderLines
	if h < 0 {
		return 0
	}
	return h
}

func CardContentWidth(columnWidth int) int {
	w := columnWidth - ColumnContentInsetX
	if w < 1 {
		return 1
	}
	return w
}

func VisibleColumnCount(totalColumns int, boardWidth int) int {
	if totalColumns <= 0 {
		return 0
	}
	if totalColumns <= MinVisibleColumns {
		return totalColumns
	}

	visible := boardWidth / MinColumnWidth
	if visible < MinVisibleColumns {
		visible = MinVisibleColumns
	}
	if visible > totalColumns {
		visible = totalColumns
	}
	return visible
}
