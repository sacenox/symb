package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// ---------------------------------------------------------------------------
// Selection and clipboard
// ---------------------------------------------------------------------------

// copySelection copies the active selection (from any component) to the
// clipboard using both OSC 52 (for SSH/tmux) and native clipboard.
func (m *Model) copySelection() tea.Cmd {
	var text string
	switch {
	case m.editor.HasSelection():
		text = m.editor.SelectedText()
	case m.agentInput.HasSelection():
		text = m.agentInput.SelectedText()
	case m.convSel != nil && !m.convSel.empty():
		text = m.selectedConvText()
	}
	if text == "" {
		return nil
	}
	return tea.SetClipboard(text) // OSC 52 — works through SSH/tmux
}

// selectedConvText returns the plain text of the conversation selection.
func (m *Model) selectedConvText() string {
	if m.convSel == nil || m.convSel.empty() {
		return ""
	}
	lines := m.wrappedConvLines()
	s, e := m.convSel.ordered()
	s.line = max(s.line, 0)
	e.line = min(e.line, len(lines)-1)

	var sb strings.Builder
	for i := s.line; i <= e.line; i++ {
		plain := ansi.Strip(lines[i])
		runes := []rune(plain)
		start := 0
		end := len(runes)
		if i == s.line {
			start = min(s.col, len(runes))
		}
		if i == e.line {
			end = min(e.col, len(runes))
		}
		sb.WriteString(string(runes[start:end]))
		if i < e.line {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// applyClickableStyle styles the first line of a clickable entry only.
func (m Model) applyClickableStyle(line string, lineIdx int, bgFill lipgloss.Style) string {
	if !m.isClickableLine(lineIdx) {
		return line
	}
	m.wrappedConvLines()
	src := m.convLineSource
	if lineIdx < 0 || lineIdx >= len(src) {
		return line
	}
	entryIdx := src[lineIdx]
	if entryIdx < 0 || entryIdx >= len(m.convEntries) {
		return line
	}
	if lineIdx > 0 && src[lineIdx-1] == entryIdx {
		return line
	}
	entry := m.convEntries[entryIdx]
	plain := ansi.Strip(line)
	if entry.kind == entryUndo {
		return styleUndoLine(plain, bgFill, m.styles.Clickable)
	}
	return m.styles.Clickable.Render(plain)
}

func styleUndoLine(plain string, bgFill, clickable lipgloss.Style) string {
	const undoLabel = "undo"
	idx := strings.LastIndex(plain, undoLabel)
	if idx == -1 {
		return clickable.Render(plain)
	}
	before := plain[:idx]
	after := plain[idx+len(undoLabel):]
	var sb strings.Builder
	if before != "" {
		sb.WriteString(bgFill.Render(before))
	}
	sb.WriteString(clickable.Render(undoLabel))
	if after != "" {
		sb.WriteString(bgFill.Render(after))
	}
	return sb.String()
}

// renderConvLine renders a single conversation line with optional selection highlight.
// Returns the styled line. Padding is handled by the caller.
func (m Model) renderConvLine(line string, lineIdx, _ int, bgFill lipgloss.Style) string {
	if m.convSel == nil || m.convSel.empty() {
		return m.applyClickableStyle(line, lineIdx, bgFill)
	}
	line = m.applyClickableStyle(line, lineIdx, bgFill)

	s, e := m.convSel.ordered()
	if lineIdx < s.line || lineIdx > e.line {
		return line
	}

	plain := ansi.Strip(line)
	runes := []rune(plain)
	lineLen := len(runes)

	// Compute selection column range for this line
	selStart := 0
	if lineIdx == s.line {
		selStart = s.col
	}
	selEnd := lineLen
	if lineIdx == e.line {
		selEnd = e.col
	}

	// Clamp
	if selStart < 0 {
		selStart = 0
	}
	if selEnd > lineLen {
		selEnd = lineLen
	}
	if selStart >= selEnd {
		return line
	}

	// Build: [before] [selected] [after]
	before := string(runes[:selStart])
	selected := string(runes[selStart:selEnd])
	after := string(runes[selEnd:])

	var sb strings.Builder
	if before != "" {
		sb.WriteString(bgFill.Render(before))
	}
	sb.WriteString(m.styles.Selection.Render(selected))
	if after != "" {
		sb.WriteString(bgFill.Render(after))
	}
	return sb.String()
}
