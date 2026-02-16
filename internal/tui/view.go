package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

func (m Model) View() tea.View {
	content := m.renderContent()
	if m.fileModal != nil {
		content = m.fileModal.View(m.width, m.height)
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeAllMotion
	// Keyboard enhancements are automatically enabled in v2 (Kitty protocol).
	// This gives us ctrl+shift+c/v disambiguation.
	return v
}

// renderContent produces the string content for the view.
func (m Model) renderContent() string {
	if m.width == 0 {
		return ""
	}

	ly := m.layout
	contentH := m.height - statusRows
	var b strings.Builder

	editorLines := strings.Split(m.editor.View(), "\n")
	inputLines := strings.Split(m.agentInput.View(), "\n")
	convLines := m.wrappedConvLines()
	startLine := m.visibleStartLine()
	bgFill := m.styles.BgFill

	for row := 0; row < contentH; row++ {
		m.renderEditorRow(&b, editorLines, row, ly.editor.Dx(), bgFill)
		b.WriteString(m.styles.Border.Render("│"))
		m.renderRightPaneRow(&b, convLines, inputLines, row, startLine, bgFill)
		b.WriteByte('\n')
	}

	m.renderStatusBar(&b, bgFill)
	return b.String()
}

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

// renderRightPaneRow writes one row of the right pane (conv / sep / input).
func (m Model) renderRightPaneRow(b *strings.Builder, convLines, inputLines []string, row, startLine int, bgFill lipgloss.Style) {
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
	line := m.renderConvLine(convLines[lineIdx], lineIdx, rw, bgFill)
	lw := lipgloss.Width(line)
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

// renderStatusBar writes the status separator and bar.
func (m Model) renderStatusBar(b *strings.Builder, bgFill lipgloss.Style) {
	divX := m.layout.div.Min.X
	b.WriteString(m.styles.Border.Render(strings.Repeat("─", divX)))
	b.WriteString(m.styles.Border.Render("┴"))
	b.WriteString(m.styles.Border.Render(strings.Repeat("─", m.width-divX-1)))
	b.WriteByte('\n')

	// -- Left segments --
	var leftParts []string

	// Git branch + dirty
	if m.gitBranch != "" {
		branch := m.gitBranch
		if m.gitDirty {
			branch += "*"
		}
		leftParts = append(leftParts, m.styles.StatusText.Render(" "+branch))
	}

	// LSP diagnostics for current editor file
	if m.lspErrors > 0 || m.lspWarnings > 0 {
		var diags []string
		if m.lspErrors > 0 {
			diags = append(diags, m.styles.Error.Render(fmt.Sprintf("✗ %d", m.lspErrors)))
		}
		if m.lspWarnings > 0 {
			diags = append(diags, m.styles.Warning.Render(fmt.Sprintf("⚠ %d", m.lspWarnings)))
		}
		leftParts = append(leftParts, strings.Join(diags, m.styles.StatusText.Render(" ")))
	} else if m.editorFilePath != "" {
		leftParts = append(leftParts, m.styles.StatusText.Render(m.editorFilePath))
	}

	left := strings.Join(leftParts, m.styles.StatusText.Render("  "))

	// -- Right segments --
	var rightParts []string

	// Network error (truncated)
	if m.lastNetError != "" {
		errText := m.lastNetError
		if len(errText) > 30 {
			errText = errText[:30] + "…"
		}
		rightParts = append(rightParts, m.styles.Error.Render("✗ "+errText))
	}

	// Provider config name
	rightParts = append(rightParts, m.styles.StatusText.Render(m.providerConfigName))

	// Animated braille spinner — red on error, accent otherwise
	frame := brailleFrames[m.spinFrame%len(brailleFrames)]
	if m.lastNetError != "" {
		frame = m.styles.Error.Render(frame)
	} else {
		frame = lipgloss.NewStyle().Background(ColorBg).Foreground(ColorHighlight).Render(frame)
	}
	rightParts = append(rightParts, frame)

	right := strings.Join(rightParts, m.styles.StatusText.Render(" "))

	// -- Compose: left + gap + right + trailing space --
	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	gap := m.width - leftW - rightW - 1
	if gap < 0 {
		gap = 0
	}
	b.WriteString(left)
	b.WriteString(bgFill.Render(strings.Repeat(" ", gap)))
	b.WriteString(right)
	b.WriteString(bgFill.Render(" "))
}
