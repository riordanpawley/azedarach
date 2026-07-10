package board

const (
	BoardStatusBarLines = 1
	ColumnHeaderLines   = 1
	ColumnContentInsetX = 2
	DefaultColumnCount  = 4
	MinColumnWidth      = 40
	SingleColumnWidth   = 72
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
	if boardWidth <= SingleColumnWidth {
		return 1
	}
	visible := boardWidth / MinColumnWidth
	if visible < 1 {
		visible = 1
	}
	if visible > totalColumns {
		visible = totalColumns
	}
	return visible
}
