package tui

import (
	tea "charm.land/bubbletea/v2"
)

// handleResize applies a window size change and re-derives layout.
func (m *Model) handleResize(msg tea.WindowSizeMsg) {
	m.width, m.height = msg.Width, msg.Height
	m.layout = generateLayout(m.width, m.height)
	m.updateComponentSizes()
}

// updateComponentSizes pushes layout dimensions to sub-models.
func (m *Model) updateComponentSizes() {
	m.agentInput.SetWidth(m.layout.input.Dx() - 2) // padding for border
	m.agentInput.SetHeight(inputRows)
}
