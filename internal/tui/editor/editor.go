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

func cachedHighlight(text, language, theme string) string {
	cacheKey := language + ":" + theme + ":" + text
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
	result := strings.TrimRight(buf.String(), "\n")

	hlCacheMu.Lock()
	if len(hlCache) > 2000 {
		hlCache = make(map[string]string)
	}
	hlCache[cacheKey] = result
	hlCacheMu.Unlock()
	return result
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

func (m *Model) clampScroll() {
	if m.height <= 0 {
		return
	}
	// Ensure cursor is visible
	if m.row < m.scroll {
		m.scroll = m.row
	}
	if m.row >= m.scroll+m.height {
		m.scroll = m.row - m.height + 1
	}
	// Don't scroll past content
	maxScroll := len(m.lines) - m.height
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

// bgForRender returns the background style. Extracts from syntax theme if
// available, falls back to BgColor.
func (m *Model) bgForRender() lipgloss.Style {
	if m.Language != "" && m.SyntaxTheme != "" {
		if hex := themeBg(m.SyntaxTheme); hex != "" {
			return lipgloss.NewStyle().Background(lipgloss.Color(hex))
		}
	}
	return lipgloss.NewStyle().Background(m.BgColor)
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
func (m *Model) screenToPos(x, y int) pos {
	row := m.scroll + y
	if row < 0 {
		row = 0
	}
	if row >= len(m.lines) {
		row = len(m.lines) - 1
	}
	col := x - m.gutterWidth
	if col < 0 {
		col = 0
	}
	lineLen := len(m.lines[row])
	if col > lineLen {
		col = lineLen
	}
	return pos{row: row, col: col}
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
			m.clampScroll()
		case tea.MouseButtonWheelDown:
			m.scroll += 3
			m.clampScroll()
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

	var b strings.Builder

	for vi := 0; vi < m.height; vi++ {
		row := m.scroll + vi
		if vi > 0 {
			b.WriteByte('\n')
		}

		if row >= len(m.lines) {
			// End-of-buffer: fill entire row with bg
			b.WriteString(bg.Render(strings.Repeat(" ", m.width)))
			continue
		}

		// -- Gutter (line numbers) -------------------------------------------
		if m.ShowLineNumbers {
			digits := m.gutterWidth - 1
			num := fmt.Sprintf("%*d ", digits, row+1)
			b.WriteString(lineNumSty.Render(num))
		}

		// -- Text content ----------------------------------------------------
		line := m.lines[row]
		lineStr := expandTabs(string(line))
		isCursorRow := m.focus && row == m.row

		var rendered string
		if isCursorRow {
			rendered = m.renderCursorLine(lineStr)
		} else if m.Language != "" && m.SyntaxTheme != "" {
			rendered = cachedHighlight(lineStr, m.Language, m.SyntaxTheme)
		} else {
			rendered = bg.Render(lineStr)
		}

		// Truncate to text width and pad
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

// renderCursorLine renders the cursor row with the cursor character visible.
func (m Model) renderCursorLine(lineStr string) string {
	bg := m.bgForRender()
	runes := []rune(lineStr)

	col := m.col
	if col > len(runes) {
		col = len(runes)
	}

	before := string(runes[:col])
	after := ""
	cursorChar := " "
	if col < len(runes) {
		cursorChar = string(runes[col])
		after = string(runes[col+1:])
	}

	// Highlight segments if syntax is enabled
	hasSyntax := m.Language != "" && m.SyntaxTheme != ""
	if hasSyntax {
		if before != "" {
			before = cachedHighlight(before, m.Language, m.SyntaxTheme)
		}
		if after != "" {
			after = cachedHighlight(after, m.Language, m.SyntaxTheme)
		}
	} else {
		before = bg.Render(before)
		after = bg.Render(after)
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
