// Package editor provides a minimal text editor component for bubbletea.
// Supports optional line numbers, Chroma syntax highlighting, mouse cursor
// placement, drag-to-select, and consistent background colors.
package editor

import (
	"fmt"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/cursor"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	LineNumStyle   lipgloss.Style // Line number gutter
	PlaceholderSty lipgloss.Style // Placeholder text
	BgColor        lipgloss.Color // Fallback bg when no syntax theme

	// Internal state
	lines  [][]rune // Backing store, one entry per line
	row    int      // Cursor row (0-indexed into lines)
	col    int      // Cursor column (0-indexed into line runes)
	scroll int      // First visible row

	width  int // Viewport width (cells)
	height int // Viewport height (rows)

	focus  bool
	cursor cursor.Model

	// Mouse selection
	selecting   bool
	selectStart pos
	selectEnd   pos

	// Cached computed values
	gutterWidth int // Width of line number gutter (0 if disabled)
}

type pos struct{ row, col int }

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
		m.gutterWidth = digits + 1 // digits + 1 space
	}
	w := m.width - m.gutterWidth
	if w < 1 {
		w = 1
	}
	return w
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

// bgHexForRender returns the background hex color. Prefers the syntax theme
// background, falls back to BgColor.
func (m *Model) bgHexForRender() string {
	if m.Language != "" && m.SyntaxTheme != "" {
		if hex := themeBg(m.SyntaxTheme); hex != "" {
			return hex
		}
	}
	return string(m.BgColor)
}

// bgForRender returns the background as a lipgloss style.
func (m *Model) bgForRender() lipgloss.Style {
	return lipgloss.NewStyle().Background(lipgloss.Color(m.bgHexForRender()))
}

// ---------------------------------------------------------------------------
// Editing operations
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Selection helpers
// ---------------------------------------------------------------------------

func (m *Model) selectionOrdered() (start, end pos) {
	s, e := m.selectStart, m.selectEnd
	if s.row > e.row || (s.row == e.row && s.col > e.col) {
		s, e = e, s
	}
	return s, e
}

func (m *Model) hasSelection() bool {
	return m.selectStart != m.selectEnd
}

func (m *Model) clearSelection() {
	m.selecting = false
	m.selectStart = pos{}
	m.selectEnd = pos{}
}

func (m *Model) selectedText() string {
	if !m.hasSelection() {
		return ""
	}
	s, e := m.selectionOrdered()
	if s.row == e.row {
		line := m.lines[s.row]
		sc := clampMax(s.col, len(line))
		ec := clampMax(e.col, len(line))
		return string(line[sc:ec])
	}
	var sb strings.Builder
	// First line from s.col to end
	first := m.lines[s.row]
	sb.WriteString(string(first[clampMax(s.col, len(first)):]))
	// Middle lines in full
	for r := s.row + 1; r < e.row; r++ {
		sb.WriteByte('\n')
		sb.WriteString(string(m.lines[r]))
	}
	// Last line from start to e.col
	sb.WriteByte('\n')
	last := m.lines[e.row]
	sb.WriteString(string(last[:clampMax(e.col, len(last))]))
	return sb.String()
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

func clampMax(v, hi int) int {
	if v < 0 {
		return 0
	}
	if v > hi {
		return hi
	}
	return v
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if !m.focus {
			break
		}
		moved := true
		switch msg.String() {
		// Navigation
		case "up":
			m.row--
			m.clampCursor()
		case "down":
			m.row++
			m.clampCursor()
		case "left":
			if m.col > 0 {
				m.col--
			} else if m.row > 0 {
				m.row--
				m.col = len(m.currentLine())
			}
		case "right":
			if m.col < len(m.currentLine()) {
				m.col++
			} else if m.row < len(m.lines)-1 {
				m.row++
				m.col = 0
			}
		case "home", "ctrl+a":
			m.col = 0
		case "end", "ctrl+e":
			m.col = len(m.currentLine())
		case "pgup":
			m.row -= m.height
			m.clampCursor()
		case "pgdown":
			m.row += m.height
			m.clampCursor()
		case "ctrl+home":
			m.row = 0
			m.col = 0
		case "ctrl+end":
			m.row = len(m.lines) - 1
			m.col = len(m.currentLine())

		// Editing
		case "backspace", "ctrl+h":
			m.deleteBack()
		case "delete", "ctrl+d":
			m.deleteForward()
		case "enter":
			m.insertNewline()
		case "tab":
			m.tabIndent()

		// Clipboard
		case "ctrl+v":
			if !m.ReadOnly {
				if text, err := clipboard.ReadAll(); err == nil {
					for _, r := range text {
						if r == '\n' {
							m.insertNewline()
						} else {
							m.insertRune(r)
						}
					}
				}
			}

		default:
			moved = false
			// Insert printable runes
			if !m.ReadOnly && len(msg.Runes) > 0 {
				for _, r := range msg.Runes {
					m.insertRune(r)
				}
				moved = true
			}
		}

		if moved {
			m.clampCursor()
			m.clampScroll()
			m.cursor.Blink = false
			cmds = append(cmds, m.cursor.BlinkCmd())
		}

	case tea.MouseMsg:
		if !m.focus {
			break
		}
		switch msg.Button {
		case tea.MouseButtonLeft:
			p := m.screenToPos(msg.X, msg.Y)
			switch msg.Action {
			case tea.MouseActionPress:
				m.selecting = true
				m.selectStart = p
				m.selectEnd = p
				m.row = p.row
				m.col = p.col
				m.clampCursor()
			case tea.MouseActionMotion:
				if m.selecting {
					m.selectEnd = p
				}
			case tea.MouseActionRelease:
				if m.selecting && m.hasSelection() {
					text := m.selectedText()
					if text != "" {
						cmds = append(cmds, func() tea.Msg {
							_ = clipboard.WriteAll(text)
							return nil
						})
					}
				}
				m.clearSelection()
			}
		case tea.MouseButtonWheelUp:
			m.scroll -= 3
			m.clampScrollBounds()
		case tea.MouseButtonWheelDown:
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
	bgHex := m.bgHexForRender()
	bg := lipgloss.NewStyle().Background(lipgloss.Color(bgHex))
	lineNumSty := m.LineNumStyle.Background(bg.GetBackground())

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
			fullHL = cachedHighlight(lineStr, m.Language, m.SyntaxTheme, bgHex)
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

		// -- Gutter (line numbers) -------------------------------------------
		if m.ShowLineNumbers {
			digits := m.gutterWidth - 1
			if vr.subRow == 0 {
				num := fmt.Sprintf("%*d ", digits, vr.bufRow+1)
				b.WriteString(lineNumSty.Render(num))
			} else {
				// Continuation row — blank gutter
				b.WriteString(lineNumSty.Render(strings.Repeat(" ", m.gutterWidth)))
			}
		}

		// -- Text content ----------------------------------------------------
		segText := vr.text
		segRuneOff := vr.subRow * tw // rune offset within the full expanded line
		segLen := len([]rune(segText))

		// Is the cursor on this visual row?
		// Cursor is here if it falls within this segment's rune range,
		// OR if it's at end-of-segment and this is the last sub-row
		// (cursor past last char sits on the last segment).
		isCursorHere := false
		if m.focus && vr.bufRow == m.row {
			if cursorExpandedCol >= segRuneOff && cursorExpandedCol < segRuneOff+tw {
				isCursorHere = true
			} else if cursorExpandedCol == segRuneOff+segLen && segLen < tw {
				// End-of-line cursor on a short (last) segment
				isCursorHere = true
			}
		}

		var rendered string
		if isCursorHere {
			localCol := cursorExpandedCol - segRuneOff
			rendered = m.renderCursorSegment(segText, vr.fullHL, vr.segStart, localCol)
		} else if hasSyntax && vr.fullHL != "" {
			rendered = ansi.Cut(vr.fullHL, vr.segStart, vr.segEnd)
		} else {
			rendered = bg.Render(segText)
		}

		// Pad to text width
		rw := lipgloss.Width(rendered)
		if rw > tw {
			rendered = ansi.Truncate(rendered, tw, "")
			rw = lipgloss.Width(rendered)
		}
		b.WriteString(rendered)
		if rw < tw {
			b.WriteString(bg.Render(strings.Repeat(" ", tw-rw)))
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
		digits := m.gutterWidth - 1
		num := fmt.Sprintf("%*d ", digits, 1)
		b.WriteString(m.LineNumStyle.Background(bg.GetBackground()).Render(num))
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
