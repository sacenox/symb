package tui

import (
	tea "charm.land/bubbletea/v2"
)

// handleResize applies a window size change and re-derives layout.
func (m *Model) handleResize(msg tea.WindowSizeMsg) {
	m.width, m.height = msg.Width, msg.Height
	if m.divX == 0 {
		m.divX = m.width / 2
	}
	if m.divX < minPaneWidth {
		m.divX = minPaneWidth
	}
	if m.divX > m.width-minPaneWidth {
		m.divX = m.width - minPaneWidth
	}
	m.layout = generateLayout(m.width, m.height, m.divX)
	m.updateComponentSizes()
}

// updateComponentSizes pushes layout dimensions to sub-models.
func (m *Model) updateComponentSizes() {
	m.editor.SetWidth(m.layout.editor.Dx())
	m.editor.SetHeight(m.layout.editor.Dy())
	m.agentInput.SetWidth(m.layout.input.Dx() - 2) // padding for border
	m.agentInput.SetHeight(inputRows)
}
