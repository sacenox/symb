package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

func (m Model) View() tea.View {
	v := tea.NewView(m.renderContent())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
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

	// Pre-split editor and input views
	editorLines := strings.Split(m.editor.View(), "\n")
	inputLines := strings.Split(m.agentInput.View(), "\n")

	// Conversation visible window (wrapped to current width)
	convLines := m.wrappedConvLines()
	startLine := m.visibleStartLine()

	bgFill := m.styles.BgFill

	for row := 0; row < contentH; row++ {
		// -- Left pane: editor -----------------------------------------------
		edW := ly.editor.Dx()
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

		// -- Divider ---------------------------------------------------------
		b.WriteString(m.styles.Border.Render("│"))

		// -- Right pane ------------------------------------------------------
		rw := m.convWidth()
		relY := row // row relative to right pane top

		if relY < ly.conv.Dy() {
			// Conversation area
			lineIdx := startLine + relY
			if lineIdx < len(convLines) {
				line := convLines[lineIdx]

				// Selection highlight (character-level)
				line = m.renderConvLine(line, lineIdx, rw, bgFill)

				lw := lipgloss.Width(line)
				b.WriteString(line)
				if lw < rw {
					b.WriteString(bgFill.Render(strings.Repeat(" ", rw-lw)))
				}
			} else {
				b.WriteString(bgFill.Render(strings.Repeat(" ", rw)))
			}

		} else if relY == ly.sep.Min.Y {
			// Separator line between conversation and input
			b.WriteString(m.styles.Border.Render(strings.Repeat("─", rw)))

		} else {
			// Input area
			inputRow := relY - ly.input.Min.Y
			if inputRow >= 0 && inputRow < len(inputLines) {
				line := inputLines[inputRow]
				lw := lipgloss.Width(line)
				if lw > rw {
					line = ansi.Truncate(line, rw, "")
					lw = lipgloss.Width(line)
				}
				b.WriteString(line)
				if lw < rw {
					b.WriteString(bgFill.Render(strings.Repeat(" ", rw-lw)))
				}
			} else {
				b.WriteString(bgFill.Render(strings.Repeat(" ", rw)))
			}
		}

		b.WriteByte('\n')
	}

	// -- Status separator: ───┴─── ------------------------------------------
	divX := ly.div.Min.X
	b.WriteString(m.styles.Border.Render(strings.Repeat("─", divX)))
	b.WriteString(m.styles.Border.Render("┴"))
	b.WriteString(m.styles.Border.Render(strings.Repeat("─", m.width-divX-1)))
	b.WriteByte('\n')

	// -- Status bar ----------------------------------------------------------
	left := m.styles.StatusText.Render(" symb")
	spin := strings.TrimSpace(m.spinner.View())
	leftW := lipgloss.Width(left)
	spinW := lipgloss.Width(spin)
	gap := m.width - leftW - spinW - 1
	if gap < 0 {
		gap = 0
	}
	b.WriteString(left)
	b.WriteString(bgFill.Render(strings.Repeat(" ", gap)))
	b.WriteString(spin)
	b.WriteString(bgFill.Render(" "))

	return b.String()
}
