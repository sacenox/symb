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

// renderConvLine renders a single conversation line with optional selection highlight.
// Returns the styled line. Padding is handled by the caller.
func (m Model) renderConvLine(line string, lineIdx, width int, bgFill lipgloss.Style) string {
	if m.convSel == nil || m.convSel.empty() {
		return line
	}
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
