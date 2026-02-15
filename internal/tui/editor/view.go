package editor

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/xonecas/symb/internal/highlight"
)

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

// visualRow is a single screen row derived from a buffer line.
type visualRow struct {
	bufRow   int    // buffer line index
	subRow   int    // 0 = first wrap segment, 1 = second, etc.
	text     string // plain text (expanded tabs) for this segment
	fullHL   string // full-line highlighted ANSI (shared across sub-rows)
	segStart int    // rune offset of this segment in the full line
	segEnd   int    // rune end offset
}

// selRange holds pre-computed selection bounds in expanded-tab space.
type selRange struct {
	startRow, startExp int
	endRow, endExp     int
}

func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	if len(m.lines) == 1 && len(m.lines[0]) == 0 && m.Placeholder != "" {
		return m.placeholderView()
	}

	tw := m.textWidth()
	bg := m.bgForRender()
	rows := m.buildVisualRows(tw)

	cursorExpandedCol := m.cursorExpanded()

	var sr *selRange
	if m.HasSelection() {
		ss, se := m.sel.ordered()
		sr = &selRange{
			startRow: ss.row, startExp: m.bufferColToExpandedCol(ss.row, ss.col),
			endRow: se.row, endExp: m.bufferColToExpandedCol(se.row, se.col),
		}
	}

	lineNumSty := m.LineNumStyle.Background(bg.GetBackground())

	var b strings.Builder
	for vi := 0; vi < m.height; vi++ {
		if vi > 0 {
			b.WriteByte('\n')
		}
		if vi >= len(rows) {
			b.WriteString(bg.Render(strings.Repeat(" ", m.width)))
			continue
		}
		vr := rows[vi]

		if m.ShowLineNumbers {
			m.renderGutter(&b, vr, lineNumSty)
		}

		rendered := m.renderSegment(vr, tw, cursorExpandedCol, sr, bg)
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

// buildVisualRows computes all visible visual rows from the scroll position.
// All visible buffer lines are highlighted as a single block so Chroma
// maintains cross-line state (important for markdown fenced blocks, but
// harmless and slightly fewer calls for other languages too).
func (m Model) buildVisualRows(tw int) []visualRow {
	hasSyntax := m.Language != "" && m.SyntaxTheme != ""
	startBuf, startRuneOff := m.visualToBuffer(m.scroll)
	startSubRow := 0
	if startRuneOff > 0 && tw > 0 {
		startSubRow = startRuneOff / tw
	}

	// First pass: collect visible buffer lines and their segments.
	type bufLine struct {
		idx      int
		text     string
		segments []string
	}
	var visible []bufLine
	rowCount := 0
	for bufIdx := startBuf; bufIdx < len(m.lines) && rowCount < m.height; bufIdx++ {
		lineStr := expandTabs(string(m.lines[bufIdx]))
		segments := wrapPlain(lineStr, tw)
		first := 0
		if bufIdx == startBuf {
			first = startSubRow
		}
		visible = append(visible, bufLine{idx: bufIdx, text: lineStr, segments: segments})
		rowCount += len(segments) - first
	}

	// Highlight all visible lines as one block.
	var hlLines []string
	if hasSyntax && len(visible) > 0 {
		block := make([]string, len(visible))
		for i, vl := range visible {
			block[i] = vl.text
		}
		joined := strings.Join(block, "\n")
		hlBlock := highlight.Highlight(joined, m.Language, m.SyntaxTheme, m.bgHexForHighlight())
		hlLines = highlight.SplitLines(hlBlock)
	}

	// Second pass: build visual rows with per-line HL from the block result.
	var rows []visualRow
	for li, vl := range visible {
		var fullHL string
		if li < len(hlLines) {
			fullHL = hlLines[li]
		}

		firstSub := 0
		if vl.idx == startBuf {
			firstSub = startSubRow
		}
		runeOff := firstSub * tw
		for subIdx := firstSub; subIdx < len(vl.segments) && len(rows) < m.height; subIdx++ {
			segLen := len([]rune(vl.segments[subIdx]))
			rows = append(rows, visualRow{
				bufRow: vl.idx, subRow: subIdx, text: vl.segments[subIdx],
				fullHL: fullHL, segStart: runeOff, segEnd: runeOff + segLen,
			})
			runeOff += segLen
		}
	}
	return rows
}

// cursorExpanded returns the cursor column in expanded-tab rune space, or -1 if unfocused.
func (m Model) cursorExpanded() int {
	if !m.focus || m.row < 0 || m.row >= len(m.lines) {
		return -1
	}
	return len([]rune(expandTabs(string(m.lines[m.row][:m.col]))))
}

// renderGutter writes the gutter (line number + marker) for one visual row.
func (m Model) renderGutter(b *strings.Builder, vr visualRow, lineNumSty lipgloss.Style) {
	gutSty := lineNumSty
	digits := m.gutterWidth - 2
	if vr.subRow == 0 {
		numSty := gutSty
		if sev, ok := m.DiagnosticLines[vr.bufRow]; ok {
			switch sev {
			case 1:
				numSty = m.DiagErrStyle.Background(gutSty.GetBackground())
			case 2:
				numSty = m.DiagWarnStyle.Background(gutSty.GetBackground())
			}
		}
		b.WriteString(numSty.Render(fmt.Sprintf("%*d ", digits, vr.bufRow+1)))
		b.WriteString(m.renderGutterMark(vr.bufRow, gutSty))
	} else {
		b.WriteString(gutSty.Render(strings.Repeat(" ", m.gutterWidth)))
	}
}

// renderSegment produces the rendered ANSI string for one visual row's text.
func (m Model) renderSegment(vr visualRow, tw, cursorExpandedCol int, sr *selRange, bg lipgloss.Style) string {
	segRuneOff := vr.subRow * tw
	segLen := len([]rune(vr.text))
	hasSyntax := m.Language != "" && m.SyntaxTheme != ""

	// Selection intersection
	rowHasSel, selColStart, selColEnd := m.segmentSelection(vr.bufRow, segRuneOff, segLen, sr)

	isCursorHere := m.isCursorOnSegment(vr.bufRow, segRuneOff, segLen, tw, cursorExpandedCol)

	if rowHasSel {
		return m.renderSelectedSegment(vr.text, vr.fullHL, vr.segStart, segLen,
			selColStart, selColEnd, m.SelectionStyle, bg, isCursorHere, cursorExpandedCol-segRuneOff)
	}
	if isCursorHere {
		return m.renderCursorSegment(vr.text, vr.fullHL, vr.segStart, cursorExpandedCol-segRuneOff)
	}
	if hasSyntax && vr.fullHL != "" {
		return ansi.Cut(vr.fullHL, vr.segStart, vr.segEnd)
	}
	return bg.Render(vr.text)
}

// segmentSelection computes selection column bounds for a segment. Returns (hasSel, start, end).
func (m Model) segmentSelection(bufRow, segRuneOff, segLen int, sr *selRange) (bool, int, int) {
	if sr == nil || bufRow < sr.startRow || bufRow > sr.endRow {
		return false, 0, 0
	}
	absSelStart := 0
	if bufRow == sr.startRow {
		absSelStart = sr.startExp
	}
	absSelEnd := segRuneOff + segLen
	if bufRow == sr.endRow {
		absSelEnd = sr.endExp
	}
	localStart := absSelStart - segRuneOff
	localEnd := absSelEnd - segRuneOff
	if localStart < 0 {
		localStart = 0
	}
	if localEnd > segLen {
		localEnd = segLen
	}
	if localStart < localEnd {
		return true, localStart, localEnd
	}
	return false, 0, 0
}

// isCursorOnSegment returns true if the cursor falls within this segment.
func (m Model) isCursorOnSegment(bufRow, segRuneOff, segLen, tw, cursorExpandedCol int) bool {
	if !m.focus || bufRow != m.row || cursorExpandedCol < 0 {
		return false
	}
	if cursorExpandedCol >= segRuneOff && cursorExpandedCol < segRuneOff+tw {
		return true
	}
	return cursorExpandedCol == segRuneOff+segLen && segLen < tw
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
	m.cursor.Style = m.CursorStyle
	cursorView := m.cursor.View()

	return before + cursorView + after
}

// segRenderer holds rendering helpers for a single segment.
type segRenderer struct {
	runes     []rune
	fullHL    string
	segStart  int
	hasSyntax bool
	bg        lipgloss.Style
}

func (sr segRenderer) renderRange(from, to int, sty lipgloss.Style) string {
	if from >= to {
		return ""
	}
	if sr.hasSyntax && sr.fullHL != "" {
		cut := ansi.Cut(sr.fullHL, sr.segStart+from, sr.segStart+to)
		return sty.Render(ansi.Strip(cut))
	}
	return sty.Render(string(sr.runes[from:to]))
}

func (sr segRenderer) renderNormal(from, to int) string {
	if from >= to {
		return ""
	}
	if sr.hasSyntax && sr.fullHL != "" {
		return ansi.Cut(sr.fullHL, sr.segStart+from, sr.segStart+to)
	}
	return sr.bg.Render(string(sr.runes[from:to]))
}

// renderSelectedSegment renders a text segment with a selection highlight
// (and optionally a cursor). selStart/selEnd are segment-local rune offsets.
func (m Model) renderSelectedSegment(
	segText, fullHL string, segStart, segLen, selStart, selEnd int,
	selSty, bg lipgloss.Style, hasCursor bool, cursorLocalCol int,
) string {
	sr := segRenderer{
		runes: []rune(segText), fullHL: fullHL, segStart: segStart,
		hasSyntax: m.Language != "" && m.SyntaxTheme != "", bg: bg,
	}

	if hasCursor && cursorLocalCol >= 0 && cursorLocalCol <= len(sr.runes) {
		return m.renderSelWithCursor(sr, selStart, selEnd, selSty, cursorLocalCol)
	}

	// No cursor: simple before/selected/after
	var sb strings.Builder
	sb.WriteString(sr.renderNormal(0, selStart))
	sb.WriteString(sr.renderRange(selStart, selEnd, selSty))
	sb.WriteString(sr.renderNormal(selEnd, segLen))
	return sb.String()
}

// renderSelWithCursor handles the cursor-in-selection case.
func (m Model) renderSelWithCursor(sr segRenderer, selStart, selEnd int, selSty lipgloss.Style, cc int) string {
	nRunes := len(sr.runes)
	cursorChar := " "
	if cc < nRunes {
		cursorChar = string(sr.runes[cc])
	}
	m.cursor.SetChar(cursorChar)
	if cc >= selStart && cc < selEnd {
		m.cursor.TextStyle = selSty
	} else {
		m.cursor.TextStyle = sr.bg
	}
	cv := m.cursor.View()

	var sb strings.Builder
	switch {
	case cc < selStart:
		sb.WriteString(sr.renderNormal(0, cc))
		sb.WriteString(cv)
		sb.WriteString(sr.renderNormal(cc+1, selStart))
		sb.WriteString(sr.renderRange(selStart, selEnd, selSty))
		sb.WriteString(sr.renderNormal(selEnd, nRunes))
	case cc >= selEnd:
		sb.WriteString(sr.renderNormal(0, selStart))
		sb.WriteString(sr.renderRange(selStart, selEnd, selSty))
		sb.WriteString(sr.renderNormal(selEnd, cc))
		sb.WriteString(cv)
		if cc+1 <= nRunes {
			sb.WriteString(sr.renderNormal(cc+1, nRunes))
		}
	default:
		sb.WriteString(sr.renderNormal(0, selStart))
		if cc > selStart {
			sb.WriteString(sr.renderRange(selStart, cc, selSty))
		}
		sb.WriteString(cv)
		if cc+1 < selEnd {
			sb.WriteString(sr.renderRange(cc+1, selEnd, selSty))
		}
		sb.WriteString(sr.renderNormal(selEnd, nRunes))
	}
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
		m.cursor.Style = m.CursorStyle
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
