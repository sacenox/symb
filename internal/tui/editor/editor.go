// Package editor provides a minimal text editor component for bubbletea.
// Supports optional line numbers, Chroma syntax highlighting, mouse cursor
// placement, drag-to-select, and consistent background colors.
package editor

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/bubbles/v2/cursor"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/xonecas/symb/internal/highlight"
)

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

// GutterMark identifies the type of change marker shown in the gutter.
type GutterMark int

const (
	GutterAdd    GutterMark = iota // Line was added
	GutterChange                   // Line was modified
	GutterDelete                   // Line(s) deleted after this line
)

// Model is a minimal text editor / viewer component.
type Model struct {
	// Public configuration — set before first Update/View.
	ReadOnly        bool
	ShowLineNumbers bool
	SubmitOnEnter   bool   // Enter is a no-op (parent handles submit); shift+enter inserts newline
	Language        string // Chroma lexer name (empty = no highlighting)
	SyntaxTheme     string // Chroma style name (empty = no highlighting)
	Placeholder     string // Shown when empty and blurred

	// Styles — set by parent.
	CursorStyle    lipgloss.Style // Foreground for the cursor character
	SelectionStyle lipgloss.Style // Background for selected text
	LineNumStyle   lipgloss.Style // Line number gutter
	PlaceholderSty lipgloss.Style // Placeholder text
	BgColor        color.Color    // Fallback bg when no syntax theme

	// Gutter markers (git diff indicators in line number column).
	GutterMarkers map[int]GutterMark // bufRow (0-indexed) -> mark type
	MarkAddStyle  lipgloss.Style     // Style for added-line marker
	MarkChgStyle  lipgloss.Style     // Style for changed-line marker
	MarkDelStyle  lipgloss.Style     // Style for deleted-line marker

	// Per-line diagnostic severity (LSP). bufRow (0-indexed) -> severity (1=error, 2=warning).
	DiagnosticLines map[int]int
	DiagErrStyle    lipgloss.Style // Line number fg when line has error
	DiagWarnStyle   lipgloss.Style // Line number fg when line has warning

	// Internal state
	lines  [][]rune // Backing store, one entry per line
	row    int      // Cursor row (0-indexed into lines)
	col    int      // Cursor column (0-indexed into line runes)
	scroll int      // First visible row

	width  int // Viewport width (cells)
	height int // Viewport height (rows)

	focus  bool
	cursor cursor.Model

	// Selection state (anchor + active pattern).
	// Anchor is where selection started; active moves with cursor/drag.
	sel      *selection
	dragging bool // Mouse drag in progress

	// Cached computed values
	gutterWidth int // Width of line number gutter (0 if disabled)
}

type pos struct{ row, col int }

// selection tracks a text selection via anchor+active points.
// Anchor is fixed (where selection started); active moves with cursor/drag.
type selection struct {
	anchor pos
	active pos
}

// ordered returns the selection endpoints in document order.
func (s selection) ordered() (start, end pos) {
	if s.anchor.row > s.active.row ||
		(s.anchor.row == s.active.row && s.anchor.col > s.active.col) {
		return s.active, s.anchor
	}
	return s.anchor, s.active
}

// empty returns true when anchor == active (no actual selection).
func (s selection) empty() bool {
	return s.anchor == s.active
}

// New creates a new editor with sensible defaults.
func New() Model {
	c := cursor.New()
	c.SetMode(cursor.CursorBlink)
	return Model{
		lines:  [][]rune{{}},
		cursor: c,
	}
}

// ---------------------------------------------------------------------------
// Public methods called by parent
// ---------------------------------------------------------------------------

func (m *Model) SetWidth(w int)  { m.width = w; m.clampScroll() }
func (m *Model) SetHeight(h int) { m.height = h; m.clampScroll() }

func (m *Model) Focus() {
	m.focus = true
	m.cursor.Focus()
}

func (m *Model) Blur() {
	m.focus = false
	m.cursor.Blur()
}

func (m Model) Focused() bool { return m.focus }

func (m *Model) SetValue(s string) {
	raw := strings.Split(s, "\n")
	m.lines = make([][]rune, len(raw))
	for i, l := range raw {
		m.lines[i] = []rune(l)
	}
	if len(m.lines) == 0 {
		m.lines = [][]rune{{}}
	}
	m.row = 0
	m.col = 0
	m.scroll = 0
}

func (m Model) Value() string {
	var sb strings.Builder
	for i, line := range m.lines {
		sb.WriteString(string(line))
		if i < len(m.lines)-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

func (m *Model) Reset() {
	m.lines = [][]rune{{}}
	m.row = 0
	m.col = 0
	m.scroll = 0
}

// GotoLine moves the cursor to the given 1-indexed line number and scrolls
// it into view.
func (m *Model) GotoLine(line int) {
	line-- // convert to 0-indexed
	if line < 0 {
		line = 0
	}
	if line >= len(m.lines) {
		line = len(m.lines) - 1
	}
	m.row = line
	m.col = 0
	m.clampScroll()
}

// Blink returns the initial cursor blink message. Call from Init().
func Blink() tea.Msg { return cursor.Blink() }

// ---------------------------------------------------------------------------
// Selection API (called by parent)
// ---------------------------------------------------------------------------

// HasSelection returns true if there is a non-empty text selection.
func (m Model) HasSelection() bool {
	return m.sel != nil && !m.sel.empty()
}

// SelectedText returns the currently selected text, or "" if none.
func (m Model) SelectedText() string {
	if !m.HasSelection() {
		return ""
	}
	s, e := m.sel.ordered()
	return m.textInRange(s, e)
}

// ClearSelection removes any active selection.
func (m *Model) ClearSelection() {
	m.sel = nil
	m.dragging = false
}

// textInRange extracts text between two buffer positions.
func (m *Model) textInRange(start, end pos) string {
	if start.row == end.row {
		line := m.lines[start.row]
		sc := clampMax(start.col, len(line))
		ec := clampMax(end.col, len(line))
		return string(line[sc:ec])
	}
	var sb strings.Builder
	first := m.lines[start.row]
	sb.WriteString(string(first[clampMax(start.col, len(first)):]))
	for r := start.row + 1; r < end.row; r++ {
		sb.WriteByte('\n')
		sb.WriteString(string(m.lines[r]))
	}
	sb.WriteByte('\n')
	last := m.lines[end.row]
	sb.WriteString(string(last[:clampMax(end.col, len(last))]))
	return sb.String()
}

// DeleteSelection removes the selected text and positions the cursor at the
// start of the deleted range. Returns true if a selection was deleted.
func (m *Model) DeleteSelection() bool {
	if m.ReadOnly || !m.HasSelection() {
		return false
	}
	s, e := m.sel.ordered()
	m.ClearSelection()

	// Clamp to valid range
	s.row = clampMax(s.row, len(m.lines)-1)
	s.col = clampMax(s.col, len(m.lines[s.row]))
	e.row = clampMax(e.row, len(m.lines)-1)
	e.col = clampMax(e.col, len(m.lines[e.row]))

	if s.row == e.row {
		line := m.lines[s.row]
		m.lines[s.row] = append(line[:s.col], line[e.col:]...)
	} else {
		// Keep text before selection start + text after selection end
		before := m.lines[s.row][:s.col]
		after := m.lines[e.row][e.col:]
		merged := make([]rune, 0, len(before)+len(after))
		merged = append(merged, before...)
		merged = append(merged, after...)
		// Replace the range of lines
		newLines := make([][]rune, 0, len(m.lines)-(e.row-s.row))
		newLines = append(newLines, m.lines[:s.row]...)
		newLines = append(newLines, merged)
		newLines = append(newLines, m.lines[e.row+1:]...)
		m.lines = newLines
	}
	if len(m.lines) == 0 {
		m.lines = [][]rune{{}}
	}
	m.row = s.row
	m.col = s.col
	return true
}

func clampMax(v, hi int) int {
	if v < 0 {
		return 0
	}
	if v > hi {
		return hi
	}
	return v
}

// startOrExtendSelection sets anchor to current cursor pos if no selection
// exists, then after the caller moves the cursor, active is updated.
func (m *Model) startOrExtendSelection() {
	if m.sel == nil {
		m.sel = &selection{
			anchor: pos{row: m.row, col: m.col},
			active: pos{row: m.row, col: m.col},
		}
	}
}

// updateSelectionActive sets the active end to the current cursor position.
func (m *Model) updateSelectionActive() {
	if m.sel != nil {
		m.sel.active = pos{row: m.row, col: m.col}
	}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func (m *Model) currentLine() []rune { return m.lines[m.row] }

func (m *Model) clampCursor() {
	if m.row < 0 {
		m.row = 0
	}
	if m.row >= len(m.lines) {
		m.row = len(m.lines) - 1
	}
	if m.col < 0 {
		m.col = 0
	}
	if m.col > len(m.currentLine()) {
		m.col = len(m.currentLine())
	}
}

// clampScrollBounds enforces scroll min/max without snapping to the cursor.
// Use for mouse wheel scrolling where the viewport moves independently.
func (m *Model) clampScrollBounds() {
	if m.height <= 0 {
		return
	}
	maxScroll := m.visualRowCount() - m.height
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.scroll > maxScroll {
		m.scroll = maxScroll
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}

// clampScroll ensures the cursor's visual row is visible and scroll doesn't
// exceed content bounds. m.scroll is a visual row offset.
func (m *Model) clampScroll() {
	if m.height <= 0 {
		return
	}
	cvr := m.cursorVisualRow()
	// Ensure cursor is visible
	if cvr < m.scroll {
		m.scroll = cvr
	}
	if cvr >= m.scroll+m.height {
		m.scroll = cvr - m.height + 1
	}
	// Don't scroll past content
	maxScroll := m.visualRowCount() - m.height
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.scroll > maxScroll {
		m.scroll = maxScroll
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}

const tabWidth = 4

// expandTabs replaces tabs with spaces (tabWidth-aligned).
func expandTabs(s string) string {
	var b strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			spaces := tabWidth - (col % tabWidth)
			b.WriteString(strings.Repeat(" ", spaces))
			col += spaces
		} else {
			b.WriteRune(r)
			col++
		}
	}
	return b.String()
}

// textWidth returns the width available for text content.
func (m *Model) textWidth() int {
	m.gutterWidth = 0
	if m.ShowLineNumbers {
		digits := len(fmt.Sprintf("%d", len(m.lines)))
		if digits < 2 {
			digits = 2
		}
		m.gutterWidth = digits + 2 // digits + marker column + 1 space
	}
	w := m.width - m.gutterWidth
	if w < 1 {
		w = 1
	}
	return w
}

// SetGutterMarkers replaces the current gutter markers.
// Keys are 0-indexed buffer row numbers.
func (m *Model) SetGutterMarkers(markers map[int]GutterMark) {
	m.GutterMarkers = markers
}

// renderGutterMark returns the styled single-character marker for a buffer row.
// Falls back to a space (in lineNumSty) if no marker exists.
func (m Model) renderGutterMark(bufRow int, lineNumSty lipgloss.Style) string {
	mark, ok := m.GutterMarkers[bufRow]
	if !ok {
		return lineNumSty.Render(" ")
	}
	switch mark {
	case GutterAdd:
		return m.MarkAddStyle.Render("+")
	case GutterChange:
		return m.MarkChgStyle.Render("~")
	case GutterDelete:
		return m.MarkDelStyle.Render("-")
	default:
		return lineNumSty.Render(" ")
	}
}

// ---------------------------------------------------------------------------
// Soft line wrapping helpers
// ---------------------------------------------------------------------------

// wrapPlain splits a plain-text string into visual rows of at most `width`
// runes. No ANSI awareness needed — call this on the raw expanded-tab text.
func wrapPlain(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	runes := []rune(s)
	if len(runes) <= width {
		return []string{s}
	}
	var rows []string
	for len(runes) > 0 {
		end := width
		if end > len(runes) {
			end = len(runes)
		}
		rows = append(rows, string(runes[:end]))
		runes = runes[end:]
	}
	return rows
}

// visualRowCount returns the total number of visual rows across all buffer
// lines, given the text width for wrapping.
func (m *Model) visualRowCount() int {
	tw := m.textWidth()
	total := 0
	for _, line := range m.lines {
		expanded := expandTabs(string(line))
		n := len([]rune(expanded))
		if n <= tw {
			total++
		} else {
			total += (n + tw - 1) / tw
		}
	}
	return total
}

// cursorVisualRow returns the visual row index of the current cursor position.
func (m *Model) cursorVisualRow() int {
	tw := m.textWidth()
	vr := 0
	for i := 0; i < m.row && i < len(m.lines); i++ {
		expanded := expandTabs(string(m.lines[i]))
		n := len([]rune(expanded))
		if n <= tw {
			vr++
		} else {
			vr += (n + tw - 1) / tw
		}
	}
	// Add the sub-row within the cursor's line
	if m.row < len(m.lines) {
		// The cursor col in the expanded string
		prefix := expandTabs(string(m.lines[m.row][:m.col]))
		runeCol := len([]rune(prefix))
		vr += runeCol / tw
	}
	return vr
}

// visualToBuffer converts a visual row index to a buffer row and the rune
// offset (in the expanded line) at the start of that visual sub-row.
func (m *Model) visualToBuffer(visRow int) (bufRow, runeOffset int) {
	tw := m.textWidth()
	vr := 0
	for i, line := range m.lines {
		expanded := expandTabs(string(line))
		n := len([]rune(expanded))
		rows := 1
		if n > tw {
			rows = (n + tw - 1) / tw
		}
		if vr+rows > visRow {
			return i, (visRow - vr) * tw
		}
		vr += rows
	}
	// Past end — return last line
	return len(m.lines) - 1, 0
}

// expandedColToBufferCol maps a column in the expanded-tab string back to the
// corresponding rune index in the original buffer line.
func (m *Model) expandedColToBufferCol(bufRow, expandedCol int) int {
	if bufRow < 0 || bufRow >= len(m.lines) {
		return 0
	}
	line := m.lines[bufRow]
	col := 0 // visual column in expanded space
	for i, r := range line {
		if col >= expandedCol {
			return i
		}
		if r == '\t' {
			col += tabWidth - (col % tabWidth)
		} else {
			col++
		}
	}
	return len(line)
}

// bufferColToExpandedCol converts a buffer column (rune index) to expanded-tab
// column (visual column) for a given buffer row.
func (m *Model) bufferColToExpandedCol(bufRow, bufCol int) int {
	if bufRow < 0 || bufRow >= len(m.lines) {
		return 0
	}
	line := m.lines[bufRow]
	c := clampMax(bufCol, len(line))
	prefix := expandTabs(string(line[:c]))
	return len([]rune(prefix))
}

// bgForRender returns the background as a lipgloss style. Prefers the syntax
// theme background, falls back to BgColor.
func (m *Model) bgForRender() lipgloss.Style {
	if m.Language != "" && m.SyntaxTheme != "" {
		if hex := highlight.ThemeBg(m.SyntaxTheme); hex != "" {
			return lipgloss.NewStyle().Background(lipgloss.Color(hex))
		}
	}
	return lipgloss.NewStyle().Background(m.BgColor)
}

// bgHexForHighlight returns the bg hex string for syntax highlighting.
func (m *Model) bgHexForHighlight() string {
	if m.Language != "" && m.SyntaxTheme != "" {
		if hex := highlight.ThemeBg(m.SyntaxTheme); hex != "" {
			return hex
		}
	}
	if m.BgColor != nil {
		r, g, b, _ := m.BgColor.RGBA()
		return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
	}
	return "#000000"
}
