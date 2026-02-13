package editor

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

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
