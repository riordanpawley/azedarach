package board

const (
	BoardStatusBarLines = 1
	ColumnHeaderLines   = 1
	ColumnContentInsetX = 2
	DefaultColumnCount  = 4
	MinColumnWidth      = 40
	SingleColumnWidth   = 72
)

// ColumnLayout is the shared horizontal geometry for a board viewport.
// Rendering, navigation, scrolling, and pointer hit-testing must consume this
// value rather than independently deriving column counts or widths.
type ColumnLayout struct {
	BoardWidth    int
	TotalColumns  int
	VisibleCount  int
	ColumnWidth   int
	ExtraColumns  int
	ViewportStart int
}

func NewColumnLayout(totalColumns, boardWidth, viewportStart int) ColumnLayout {
	visibleCount := VisibleColumnCount(totalColumns, boardWidth)
	layout := NewVisibleColumnLayout(visibleCount, boardWidth)
	layout.TotalColumns = totalColumns
	if visibleCount > 0 {
		maxStart := totalColumns - visibleCount
		if viewportStart < 0 {
			viewportStart = 0
		} else if viewportStart > maxStart {
			viewportStart = maxStart
		}
	} else {
		viewportStart = 0
	}
	layout.ViewportStart = viewportStart
	return layout
}

// NewVisibleColumnLayout returns exact geometry for a slice of columns that
// has already been selected for rendering.
func NewVisibleColumnLayout(visibleCount, boardWidth int) ColumnLayout {
	if visibleCount < 0 {
		visibleCount = 0
	}
	columnWidth := 0
	if visibleCount > 0 {
		columnWidth = boardWidth / visibleCount
		if columnWidth < 1 {
			columnWidth = 1
		}
	}
	return ColumnLayout{
		BoardWidth:   boardWidth,
		TotalColumns: visibleCount,
		VisibleCount: visibleCount,
		ColumnWidth:  columnWidth,
		ExtraColumns: boardWidth - (columnWidth * visibleCount),
	}
}

func (l ColumnLayout) Range() (int, int) {
	return l.ViewportStart, l.ViewportStart + l.VisibleCount
}

func (l ColumnLayout) WithColumnVisible(column int) ColumnLayout {
	if column < 0 || column >= l.TotalColumns || l.VisibleCount < 1 {
		return l
	}
	start := l.ViewportStart
	if column < start {
		start = column
	} else if column >= start+l.VisibleCount {
		start = column - l.VisibleCount + 1
	}
	return NewColumnLayout(l.TotalColumns, l.BoardWidth, start)
}

func (l ColumnLayout) ColumnAt(x int) (int, bool) {
	if x < 0 || x >= l.BoardWidth || l.VisibleCount < 1 || l.ColumnWidth < 1 {
		return 0, false
	}
	offset := 0
	for localColumn := 0; localColumn < l.VisibleCount; localColumn++ {
		offset += l.WidthForLocalColumn(localColumn)
		if x < offset {
			return l.ViewportStart + localColumn, true
		}
	}
	return 0, false
}

func (l ColumnLayout) WidthForLocalColumn(localColumn int) int {
	if localColumn < 0 || localColumn >= l.VisibleCount {
		return 0
	}
	width := l.ColumnWidth
	if localColumn < l.ExtraColumns {
		width++
	}
	return width
}

func (l ColumnLayout) WidthForColumn(column int) int {
	return l.WidthForLocalColumn(column - l.ViewportStart)
}

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
