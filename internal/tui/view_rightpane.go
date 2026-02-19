package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// renderPaneRow writes one row of the single pane (conv / sep / input).
func (m Model) renderPaneRow(b *strings.Builder, convLines, inputLines []string, row, startLine int, bgFill lipgloss.Style) {
	ly := m.layout
	rw := m.convWidth()

	switch {
	case row < ly.conv.Dy():
		m.renderConvRow(b, convLines, startLine+row, rw, bgFill)
	case row == ly.sep.Min.Y:
		b.WriteString(m.styles.Border.Render(strings.Repeat("─", rw)))
	default:
		renderPaddedLine(b, inputLines, row-ly.input.Min.Y, rw, bgFill)
	}
}

// renderConvRow writes one conversation line with selection highlight.
func (m Model) renderConvRow(b *strings.Builder, convLines []string, lineIdx, rw int, bgFill lipgloss.Style) {
	if lineIdx >= len(convLines) {
		b.WriteString(bgFill.Render(strings.Repeat(" ", rw)))
		return
	}
	line := m.renderConvLine(convLines[lineIdx], lineIdx, bgFill)
	lw := lipgloss.Width(line)

	// Center separator and undo entries.
	if m.isCentered(lineIdx) {
		pad := (rw - lw) / 2
		if pad > 0 {
			b.WriteString(bgFill.Render(strings.Repeat(" ", pad)))
			b.WriteString(line)
			trail := rw - pad - lw
			if trail > 0 {
				b.WriteString(bgFill.Render(strings.Repeat(" ", trail)))
			}
			return
		}
	}

	b.WriteString(line)
	if lw < rw {
		b.WriteString(bgFill.Render(strings.Repeat(" ", rw-lw)))
	}
}

// renderPaddedLine writes a line from lines[idx] padded/truncated to width,
// or a blank fill if idx is out of range.
func renderPaddedLine(b *strings.Builder, lines []string, idx, width int, bgFill lipgloss.Style) {
	if idx >= 0 && idx < len(lines) {
		line := lines[idx]
		lw := lipgloss.Width(line)
		if lw > width {
			line = ansi.Truncate(line, width, "")
			lw = lipgloss.Width(line)
		}
		b.WriteString(line)
		if lw < width {
			b.WriteString(bgFill.Render(strings.Repeat(" ", width-lw)))
		}
	} else {
		b.WriteString(bgFill.Render(strings.Repeat(" ", width)))
	}
}
