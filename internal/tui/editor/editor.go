// Package editor provides a minimal text editor component for bubbletea.
// Supports optional line numbers, Chroma syntax highlighting, mouse cursor
// placement, drag-to-select, and consistent background colors.
package editor

import (
	"fmt"
	"image/color"
	"strings"
	"sync"

	"charm.land/bubbles/v2/cursor"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"
)

// ---------------------------------------------------------------------------
// Highlight cache (global, shared across instances)
// ---------------------------------------------------------------------------

var (
	hlCache   = make(map[string]string)
	hlCacheMu sync.RWMutex
)

func cachedHighlight(text, language, theme, bgHex string) string {
	cacheKey := language + ":" + theme + ":" + bgHex + ":" + text
	hlCacheMu.RLock()
	if v, ok := hlCache[cacheKey]; ok {
		hlCacheMu.RUnlock()
		return v
	}
	hlCacheMu.RUnlock()

	lex := lexers.Get(language)
	if lex == nil {
		return text
	}
	lex = chroma.Coalesce(lex)
	sty := styles.Get(theme)
	fmtr := formatters.Get("terminal16m")
	if fmtr == nil {
		fmtr = formatters.Fallback
	}
	it, err := lex.Tokenise(nil, text)
	if err != nil {
		return text
	}
	var buf strings.Builder
	if err := fmtr.Format(&buf, sty, it); err != nil {
		return text
	}
	raw := strings.TrimRight(buf.String(), "\n")

	// Chroma's terminal16m formatter skips bg on tokens that inherit from
	// the Background entry, and every \x1b[0m reset clears bg. Fix by
	// replacing resets with reset+bg so the background is always active.
	bgSeq := hexToBgSeq(bgHex)
	result := bgSeq + strings.ReplaceAll(raw, "\x1b[0m", "\x1b[0m"+bgSeq)

	hlCacheMu.Lock()
	if len(hlCache) > 2000 {
		hlCache = make(map[string]string)
	}
	hlCache[cacheKey] = result
	hlCacheMu.Unlock()
	return result
}

// hexToBgSeq converts "#rrggbb" to an ANSI 24-bit background escape sequence.
func hexToBgSeq(hex string) string {
	if len(hex) != 7 || hex[0] != '#' {
		return ""
	}
	r := hexByte(hex[1], hex[2])
	g := hexByte(hex[3], hex[4])
	b := hexByte(hex[5], hex[6])
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r, g, b)
}

func hexByte(hi, lo byte) int {
	return hexNibble(hi)<<4 | hexNibble(lo)
}

func hexNibble(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return 0
}

// colorToBgSeq converts a color.Color to an ANSI 24-bit background escape.
func colorToBgSeq(c color.Color) string {
	if c == nil {
		return ""
	}
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r>>8, g>>8, b>>8)
}

// themeBg extracts the background hex color from a Chroma style.
// Returns "" if no background is set.
func themeBg(theme string) string {
	sty := styles.Get(theme)
	if sty == nil {
		return ""
	}
	bg := sty.Get(chroma.Background).Background
	if !bg.IsSet() {
		return ""
	}
	return bg.String() // "#rrggbb"
}

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

	// Per-line background overrides (e.g. diff line tinting).
	LineBg map[int]lipgloss.Style // bufRow (0-indexed) -> background style

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

// SetLineBg replaces the per-line background overrides.
// Keys are 0-indexed buffer row numbers.
func (m *Model) SetLineBg(bg map[int]lipgloss.Style) {
	m.LineBg = bg
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
		if hex := themeBg(m.SyntaxTheme); hex != "" {
			return lipgloss.NewStyle().Background(lipgloss.Color(hex))
		}
	}
	return lipgloss.NewStyle().Background(m.BgColor)
}

// bgHexForHighlight returns the bg hex string for syntax highlighting.
func (m *Model) bgHexForHighlight() string {
	if m.Language != "" && m.SyntaxTheme != "" {
		if hex := themeBg(m.SyntaxTheme); hex != "" {
			return hex
		}
	}
	if m.BgColor != nil {
		r, g, b, _ := m.BgColor.RGBA()
		return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
	}
	return "#000000"
}

// ---------------------------------------------------------------------------
// Editing operations
// ---------------------------------------------------------------------------

// InsertText inserts a multi-line string at the current cursor position.
// Newlines are handled via insertNewline. No-op if ReadOnly.
func (m *Model) InsertText(text string) {
	if m.ReadOnly {
		return
	}
	for _, r := range text {
		switch r {
		case '\n':
			m.insertNewline()
		case '\r':
			// Skip carriage returns (normalize \r\n to \n)
		default:
			m.insertRune(r)
		}
	}
}

func (m *Model) insertRune(r rune) {
	if m.ReadOnly {
		return
	}
	line := m.currentLine()
	newLine := make([]rune, 0, len(line)+1)
	newLine = append(newLine, line[:m.col]...)
	newLine = append(newLine, r)
	newLine = append(newLine, line[m.col:]...)
	m.lines[m.row] = newLine
	m.col++
}

func (m *Model) insertNewline() {
	if m.ReadOnly {
		return
	}
	line := m.currentLine()
	after := make([]rune, len(line[m.col:]))
	copy(after, line[m.col:])
	m.lines[m.row] = line[:m.col]
	// Insert new line after current
	newLines := make([][]rune, 0, len(m.lines)+1)
	newLines = append(newLines, m.lines[:m.row+1]...)
	newLines = append(newLines, after)
	newLines = append(newLines, m.lines[m.row+1:]...)
	m.lines = newLines
	m.row++
	m.col = 0
}

func (m *Model) deleteBack() {
	if m.ReadOnly {
		return
	}
	if m.col > 0 {
		line := m.currentLine()
		m.lines[m.row] = append(line[:m.col-1], line[m.col:]...)
		m.col--
	} else if m.row > 0 {
		// Merge with previous line
		prev := m.lines[m.row-1]
		m.col = len(prev)
		m.lines[m.row-1] = append(prev, m.currentLine()...)
		m.lines = append(m.lines[:m.row], m.lines[m.row+1:]...)
		m.row--
	}
}

func (m *Model) deleteForward() {
	if m.ReadOnly {
		return
	}
	line := m.currentLine()
	if m.col < len(line) {
		m.lines[m.row] = append(line[:m.col], line[m.col+1:]...)
	} else if m.row < len(m.lines)-1 {
		// Merge with next line
		m.lines[m.row] = append(line, m.lines[m.row+1]...)
		m.lines = append(m.lines[:m.row+1], m.lines[m.row+2:]...)
	}
}

func (m *Model) tabIndent() {
	if m.ReadOnly {
		return
	}
	// Indent to match the leading whitespace of the line above.
	indent := "\t"
	if m.row > 0 {
		above := m.lines[m.row-1]
		indent = ""
		for _, r := range above {
			if r == ' ' || r == '\t' {
				indent += string(r)
			} else {
				break
			}
		}
		if indent == "" {
			indent = "\t"
		}
	}
	for _, r := range indent {
		m.insertRune(r)
	}
}

// screenToPos converts screen-relative x,y to a buffer row,col.
// x,y are relative to the editor component origin.
func (m *Model) screenToPos(x, y int) pos {
	visRow := m.scroll + y
	if visRow < 0 {
		visRow = 0
	}
	bufRow, runeOffset := m.visualToBuffer(visRow)

	col := x - m.gutterWidth
	if col < 0 {
		col = 0
	}
	// runeOffset is in expanded-tab space; convert back to buffer col.
	// We need to find which buffer rune corresponds to runeOffset + col
	// in the expanded string.
	bufCol := m.expandedColToBufferCol(bufRow, runeOffset+col)
	return pos{row: bufRow, col: bufCol}
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if !m.focus {
			break
		}
		moved := true
		key := msg.Keystroke()

		switch key {
		// --- Shift+navigation: extend selection ---
		case "shift+up":
			m.startOrExtendSelection()
			m.row--
			m.clampCursor()
			m.updateSelectionActive()
		case "shift+down":
			m.startOrExtendSelection()
			m.row++
			m.clampCursor()
			m.updateSelectionActive()
		case "shift+left":
			m.startOrExtendSelection()
			if m.col > 0 {
				m.col--
			} else if m.row > 0 {
				m.row--
				m.col = len(m.currentLine())
			}
			m.updateSelectionActive()
		case "shift+right":
			m.startOrExtendSelection()
			if m.col < len(m.currentLine()) {
				m.col++
			} else if m.row < len(m.lines)-1 {
				m.row++
				m.col = 0
			}
			m.updateSelectionActive()
		case "shift+home":
			m.startOrExtendSelection()
			m.col = 0
			m.updateSelectionActive()
		case "shift+end":
			m.startOrExtendSelection()
			m.col = len(m.currentLine())
			m.updateSelectionActive()
		case "shift+pgup":
			m.startOrExtendSelection()
			m.row -= m.height
			m.clampCursor()
			m.updateSelectionActive()
		case "shift+pgdown":
			m.startOrExtendSelection()
			m.row += m.height
			m.clampCursor()
			m.updateSelectionActive()

		// --- Plain navigation: clear selection ---
		case "up":
			m.ClearSelection()
			m.row--
			m.clampCursor()
		case "down":
			m.ClearSelection()
			m.row++
			m.clampCursor()
		case "left":
			m.ClearSelection()
			if m.col > 0 {
				m.col--
			} else if m.row > 0 {
				m.row--
				m.col = len(m.currentLine())
			}
		case "right":
			m.ClearSelection()
			if m.col < len(m.currentLine()) {
				m.col++
			} else if m.row < len(m.lines)-1 {
				m.row++
				m.col = 0
			}
		case "home", "ctrl+a":
			m.ClearSelection()
			m.col = 0
		case "end", "ctrl+e":
			m.ClearSelection()
			m.col = len(m.currentLine())
		case "pgup":
			m.ClearSelection()
			m.row -= m.height
			m.clampCursor()
		case "pgdown":
			m.ClearSelection()
			m.row += m.height
			m.clampCursor()
		case "ctrl+home":
			m.ClearSelection()
			m.row = 0
			m.col = 0
		case "ctrl+end":
			m.ClearSelection()
			m.row = len(m.lines) - 1
			m.col = len(m.currentLine())

		// --- Editing: delete selection first ---
		case "backspace", "ctrl+h":
			if m.HasSelection() {
				m.DeleteSelection()
			} else {
				m.deleteBack()
			}
		case "delete", "ctrl+d":
			if m.HasSelection() {
				m.DeleteSelection()
			} else {
				m.deleteForward()
			}
		case "enter":
			m.DeleteSelection()
			m.insertNewline()
		case "tab":
			m.DeleteSelection()
			m.tabIndent()

		default:
			moved = false
			if !m.ReadOnly && msg.Text != "" {
				m.DeleteSelection()
				for _, r := range msg.Text {
					m.insertRune(r)
				}
				moved = true
			}
		}

		if moved {
			m.clampCursor()
			m.clampScroll()
			cmds = append(cmds, m.cursor.Blink())
		}

	case tea.MouseClickMsg:
		if !m.focus {
			break
		}
		if msg.Button == tea.MouseLeft {
			p := m.screenToPos(msg.X, msg.Y)
			m.dragging = true
			m.sel = &selection{anchor: p, active: p}
			m.row = p.row
			m.col = p.col
			m.clampCursor()
		}

	case tea.MouseMotionMsg:
		if !m.focus {
			break
		}
		if m.dragging {
			p := m.screenToPos(msg.X, msg.Y)
			m.sel.active = p
			m.row = p.row
			m.col = p.col
			m.clampCursor()
		}

	case tea.MouseReleaseMsg:
		if !m.focus {
			break
		}
		m.dragging = false
		if m.sel != nil && m.sel.empty() {
			m.ClearSelection()
		}

	case tea.MouseWheelMsg:
		if !m.focus {
			break
		}
		if msg.Button == tea.MouseWheelUp {
			m.scroll -= 3
			m.clampScrollBounds()
		} else if msg.Button == tea.MouseWheelDown {
			m.scroll += 3
			m.clampScrollBounds()
		}
	}

	// Forward to cursor for blink handling
	var cmd tea.Cmd
	m.cursor, cmd = m.cursor.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}

	// Show placeholder when empty
	if len(m.lines) == 1 && len(m.lines[0]) == 0 && m.Placeholder != "" {
		return m.placeholderView()
	}

	tw := m.textWidth()
	bg := m.bgForRender()
	lineNumSty := m.LineNumStyle.Background(bg.GetBackground())
	themeBgSeq := hexToBgSeq(m.bgHexForHighlight()) // for LineBg replacement

	// Build a flat list of visual rows from visible buffer lines.
	type visualRow struct {
		bufRow   int    // buffer line index
		subRow   int    // 0 = first wrap segment, 1 = second, etc.
		text     string // plain text (expanded tabs) for this segment
		fullHL   string // full-line highlighted ANSI (shared across sub-rows)
		segStart int    // rune offset of this segment in the full line
		segEnd   int    // rune end offset
	}

	var rows []visualRow
	hasSyntax := m.Language != "" && m.SyntaxTheme != ""

	// Find which buffer line the scroll offset lands in.
	startBuf, startRuneOff := m.visualToBuffer(m.scroll)

	// Pre-compute the sub-row index for the starting buffer line.
	startSubRow := 0
	if startRuneOff > 0 && tw > 0 {
		startSubRow = startRuneOff / tw
	}

	// Generate visual rows starting from the scroll position.
	for bufIdx := startBuf; bufIdx < len(m.lines) && len(rows) < m.height; bufIdx++ {
		lineStr := expandTabs(string(m.lines[bufIdx]))
		segments := wrapPlain(lineStr, tw)

		// Highlight the full line once for all its segments.
		var fullHL string
		if hasSyntax {
			fullHL = cachedHighlight(lineStr, m.Language, m.SyntaxTheme, m.bgHexForHighlight())
		}

		firstSub := 0
		if bufIdx == startBuf {
			firstSub = startSubRow
		}
		runeOff := firstSub * tw
		for subIdx := firstSub; subIdx < len(segments) && len(rows) < m.height; subIdx++ {
			segLen := len([]rune(segments[subIdx]))
			rows = append(rows, visualRow{
				bufRow:   bufIdx,
				subRow:   subIdx,
				text:     segments[subIdx],
				fullHL:   fullHL,
				segStart: runeOff,
				segEnd:   runeOff + segLen,
			})
			runeOff += segLen
		}
	}

	// Determine cursor position in expanded-tab space for the cursor line.
	cursorExpandedCol := -1
	if m.focus && m.row >= 0 && m.row < len(m.lines) {
		prefix := expandTabs(string(m.lines[m.row][:m.col]))
		cursorExpandedCol = len([]rune(prefix))
	}

	// Pre-compute selection range in expanded-tab space (once, not per row).
	hasSel := m.HasSelection()
	var selStartRow, selStartExp, selEndRow, selEndExp int
	if hasSel {
		ss, se := m.sel.ordered()
		selStartRow = ss.row
		selStartExp = m.bufferColToExpandedCol(ss.row, ss.col)
		selEndRow = se.row
		selEndExp = m.bufferColToExpandedCol(se.row, se.col)
	}

	selSty := m.SelectionStyle

	var b strings.Builder

	for vi := 0; vi < m.height; vi++ {
		if vi > 0 {
			b.WriteByte('\n')
		}

		if vi >= len(rows) {
			// End-of-buffer: fill entire row with bg
			b.WriteString(bg.Render(strings.Repeat(" ", m.width)))
			continue
		}

		vr := rows[vi]

		// Resolve per-line background override.
		rowBg := bg
		hasLineBg := false
		if lbg, ok := m.LineBg[vr.bufRow]; ok {
			rowBg = lbg
			hasLineBg = true
		}

		// -- Gutter (line numbers + marker column) ---------------------------
		if m.ShowLineNumbers {
			gutSty := lineNumSty
			if hasLineBg {
				gutSty = gutSty.Background(rowBg.GetBackground())
			}
			digits := m.gutterWidth - 2 // gutter = digits + space + marker
			if vr.subRow == 0 {
				num := fmt.Sprintf("%*d ", digits, vr.bufRow+1)
				b.WriteString(gutSty.Render(num))
				b.WriteString(m.renderGutterMark(vr.bufRow, gutSty))
			} else {
				// Continuation row — blank gutter
				b.WriteString(gutSty.Render(strings.Repeat(" ", m.gutterWidth)))
			}
		}

		// -- Text content ----------------------------------------------------
		segText := vr.text
		segRuneOff := vr.subRow * tw // rune offset within the full expanded line
		segLen := len([]rune(segText))

		// Compute selection intersection for this visual row.
		// selColStart/selColEnd are in segment-local expanded rune space.
		rowHasSel := false
		selColStart, selColEnd := 0, 0
		if hasSel && vr.bufRow >= selStartRow && vr.bufRow <= selEndRow {
			// Compute absolute expanded range that is selected on this buffer row.
			absSelStart := 0
			if vr.bufRow == selStartRow {
				absSelStart = selStartExp
			}
			absSelEnd := segRuneOff + segLen // default: rest of line
			if vr.bufRow == selEndRow {
				absSelEnd = selEndExp
			}
			// Intersect with this segment's range [segRuneOff, segRuneOff+segLen)
			localStart := absSelStart - segRuneOff
			localEnd := absSelEnd - segRuneOff
			if localStart < 0 {
				localStart = 0
			}
			if localEnd > segLen {
				localEnd = segLen
			}
			if localStart < localEnd {
				rowHasSel = true
				selColStart = localStart
				selColEnd = localEnd
			}
		}

		// Is the cursor on this visual row?
		isCursorHere := false
		if m.focus && vr.bufRow == m.row {
			if cursorExpandedCol >= segRuneOff && cursorExpandedCol < segRuneOff+tw {
				isCursorHere = true
			} else if cursorExpandedCol == segRuneOff+segLen && segLen < tw {
				isCursorHere = true
			}
		}

		var rendered string
		if rowHasSel {
			rendered = m.renderSelectedSegment(segText, vr.fullHL, vr.segStart, segLen,
				selColStart, selColEnd, selSty, rowBg, isCursorHere, cursorExpandedCol-segRuneOff)
		} else if isCursorHere {
			localCol := cursorExpandedCol - segRuneOff
			rendered = m.renderCursorSegment(segText, vr.fullHL, vr.segStart, localCol)
		} else if hasSyntax && vr.fullHL != "" {
			rendered = ansi.Cut(vr.fullHL, vr.segStart, vr.segEnd)
			if hasLineBg && themeBgSeq != "" {
				overrideBgSeq := colorToBgSeq(rowBg.GetBackground())
				rendered = strings.ReplaceAll(rendered, themeBgSeq, overrideBgSeq)
			}
		} else {
			rendered = rowBg.Render(segText)
		}

		// Pad to text width
		rw := lipgloss.Width(rendered)
		if rw > tw {
			rendered = ansi.Truncate(rendered, tw, "")
			rw = lipgloss.Width(rendered)
		}
		b.WriteString(rendered)
		if rw < tw {
			b.WriteString(rowBg.Render(strings.Repeat(" ", tw-rw)))
		}
	}

	return b.String()
}

// renderCursorSegment renders a text segment with the cursor at localCol.
// localCol is a rune index within the segment's plain text.
// fullHL is the full-line highlighted ANSI string; segStart is the rune offset
// of this segment within it. Uses ansi.Cut to extract correctly-highlighted
// before/after portions so syntax coloring is never broken.
func (m Model) renderCursorSegment(segText, fullHL string, segStart, localCol int) string {
	bg := m.bgForRender()
	runes := []rune(segText)

	col := localCol
	if col > len(runes) {
		col = len(runes)
	}

	// Extract the cursor character from the plain text.
	cursorChar := " "
	if col < len(runes) {
		cursorChar = string(runes[col])
	}

	hasSyntax := m.Language != "" && m.SyntaxTheme != ""
	var before, after string

	if hasSyntax && fullHL != "" {
		// Cut from the full-line highlight at absolute positions.
		absCursorCol := segStart + col
		before = ansi.Cut(fullHL, segStart, absCursorCol)
		after = ansi.Cut(fullHL, absCursorCol+1, segStart+len(runes))
	} else {
		highlighted := bg.Render(segText)
		before = ansi.Truncate(highlighted, col, "")
		after = ansi.TruncateLeft(highlighted, col+1, "")
	}

	// Render cursor character
	m.cursor.SetChar(cursorChar)
	m.cursor.TextStyle = bg
	cursorView := m.cursor.View()

	return before + cursorView + after
}

// renderSelectedSegment renders a text segment with a selection highlight
// (and optionally a cursor). selStart/selEnd are segment-local rune offsets.
func (m Model) renderSelectedSegment(
	segText, fullHL string, segStart, segLen, selStart, selEnd int,
	selSty, bg lipgloss.Style, hasCursor bool, cursorLocalCol int,
) string {
	hasSyntax := m.Language != "" && m.SyntaxTheme != ""
	runes := []rune(segText)

	// Helper: render a rune range with a given style (selection or bg).
	renderRange := func(from, to int, sty lipgloss.Style) string {
		if from >= to {
			return ""
		}
		if hasSyntax && fullHL != "" {
			cut := ansi.Cut(fullHL, segStart+from, segStart+to)
			// Wrap in selection bg — this overrides the syntax bg
			return sty.Render(ansi.Strip(cut))
		}
		return sty.Render(string(runes[from:to]))
	}

	// Helper: render a rune range with the normal style (bg or syntax HL).
	renderNormal := func(from, to int) string {
		if from >= to {
			return ""
		}
		if hasSyntax && fullHL != "" {
			return ansi.Cut(fullHL, segStart+from, segStart+to)
		}
		return bg.Render(string(runes[from:to]))
	}

	// If cursor is in this segment, we need to handle it specially.
	if hasCursor && cursorLocalCol >= 0 && cursorLocalCol <= len(runes) {
		cc := cursorLocalCol
		cursorChar := " "
		if cc < len(runes) {
			cursorChar = string(runes[cc])
		}
		m.cursor.SetChar(cursorChar)
		cursorInSel := cc >= selStart && cc < selEnd
		if cursorInSel {
			m.cursor.TextStyle = selSty
		} else {
			m.cursor.TextStyle = bg
		}
		cv := m.cursor.View()

		var sb strings.Builder
		switch {
		case cc < selStart:
			// [normal 0..cc] [cursor] [normal cc+1..selStart] [sel] [normal selEnd..end]
			sb.WriteString(renderNormal(0, cc))
			sb.WriteString(cv)
			sb.WriteString(renderNormal(cc+1, selStart))
			sb.WriteString(renderRange(selStart, selEnd, selSty))
			sb.WriteString(renderNormal(selEnd, len(runes)))
		case cc >= selEnd:
			// [normal 0..selStart] [sel] [normal selEnd..cc] [cursor] [normal cc+1..end]
			sb.WriteString(renderNormal(0, selStart))
			sb.WriteString(renderRange(selStart, selEnd, selSty))
			sb.WriteString(renderNormal(selEnd, cc))
			sb.WriteString(cv)
			if cc+1 <= len(runes) {
				sb.WriteString(renderNormal(cc+1, len(runes)))
			}
		default:
			// Cursor inside selection
			// [normal 0..selStart] [sel selStart..cc] [cursor] [sel cc+1..selEnd] [normal selEnd..end]
			sb.WriteString(renderNormal(0, selStart))
			if cc > selStart {
				sb.WriteString(renderRange(selStart, cc, selSty))
			}
			sb.WriteString(cv)
			if cc+1 < selEnd {
				sb.WriteString(renderRange(cc+1, selEnd, selSty))
			}
			sb.WriteString(renderNormal(selEnd, len(runes)))
		}
		return sb.String()
	}

	// No cursor: simple before/selected/after
	var sb strings.Builder
	sb.WriteString(renderNormal(0, selStart))
	sb.WriteString(renderRange(selStart, selEnd, selSty))
	sb.WriteString(renderNormal(selEnd, segLen))
	return sb.String()
}

// ---------------------------------------------------------------------------
// Placeholder view (shown when empty + unfocused)
// ---------------------------------------------------------------------------

func (m Model) placeholderView() string {
	if m.Placeholder == "" {
		return ""
	}
	bg := m.bgForRender()
	tw := m.textWidth()

	var b strings.Builder
	// Gutter
	if m.ShowLineNumbers {
		lineNumSty := m.LineNumStyle.Background(bg.GetBackground())
		digits := m.gutterWidth - 2 // gutter = digits + space + marker
		num := fmt.Sprintf("%*d ", digits, 1)
		b.WriteString(lineNumSty.Render(num))
		b.WriteString(m.renderGutterMark(0, lineNumSty))
	}

	// First line: cursor (if focused) then placeholder text
	if m.focus {
		// Render cursor on first character of placeholder
		phRunes := []rune(m.Placeholder)
		m.cursor.SetChar(string(phRunes[0]))
		m.cursor.TextStyle = m.PlaceholderSty
		b.WriteString(m.cursor.View())
		rest := m.PlaceholderSty.Render(string(phRunes[1:]))
		rw := lipgloss.Width(m.cursor.View()) + lipgloss.Width(rest)
		b.WriteString(rest)
		if rw < tw {
			b.WriteString(bg.Render(strings.Repeat(" ", tw-rw)))
		}
	} else {
		ph := m.PlaceholderSty.Render(m.Placeholder)
		pw := lipgloss.Width(ph)
		b.WriteString(ph)
		if pw < tw {
			b.WriteString(bg.Render(strings.Repeat(" ", tw-pw)))
		}
	}

	// Remaining rows: empty with bg
	for vi := 1; vi < m.height; vi++ {
		b.WriteByte('\n')
		b.WriteString(bg.Render(strings.Repeat(" ", m.width)))
	}

	return b.String()
}
