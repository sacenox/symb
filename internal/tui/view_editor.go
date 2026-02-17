package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// renderEditorRow writes one row of the left (editor) pane to b.
func (m Model) renderEditorRow(b *strings.Builder, editorLines []string, row, edW int, bgFill lipgloss.Style) {
	if row < len(editorLines) {
		line := editorLines[row]
		lw := lipgloss.Width(line)
		if lw > edW {
			line = ansi.Truncate(line, edW, "")
			lw = lipgloss.Width(line)
		}
		b.WriteString(line)
		if lw < edW {
			b.WriteString(bgFill.Render(strings.Repeat(" ", edW-lw)))
		}
	} else {
		b.WriteString(bgFill.Render(strings.Repeat(" ", edW)))
	}
}
