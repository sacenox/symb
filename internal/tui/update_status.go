package tui

import (
	tea "charm.land/bubbletea/v2"
)

// handleLSPDiag is a no-op now that the editor pane is removed.
func (m *Model) handleLSPDiag(_ LSPDiagnosticsMsg) Model {
	return *m
}

// handleGitBranch updates statusbar git state and schedules the next poll.
func (m Model) handleGitBranch(msg gitBranchMsg) (tea.Model, tea.Cmd) {
	m.gitBranch = msg.branch
	m.gitDirty = msg.dirty
	return m, gitBranchTick()
}
