package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

func (m Model) View() tea.View {
	content := m.renderContent()
	switch {
	case m.keybindsModal != nil:
		content = m.keybindsModal.View(m.width, m.height)
	case m.fileModal != nil:
		content = m.fileModal.View(m.width, m.height)
	case m.modelsModal != nil:
		content = m.modelsModal.View(m.width, m.height)
	case m.toolViewModal != nil:
		content = m.toolViewModal.View(m.width, m.height)
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeAllMotion
	return v
}

// renderContent produces the string content for the view.
func (m Model) renderContent() string {
	if m.width == 0 {
		return ""
	}

	contentH := m.height - statusRows
	var b strings.Builder

	inputLines := strings.Split(m.agentInput.View(), "\n")
	convLines := m.wrappedConvLines()
	startLine := m.visibleStartLine()
	bgFill := m.styles.BgFill

	for row := 0; row < contentH; row++ {
		m.renderPaneRow(&b, convLines, inputLines, row, startLine, bgFill)
		b.WriteByte('\n')
	}

	m.renderStatusBar(&b, bgFill)
	return b.String()
}
