package tui

import (
	tea "charm.land/bubbletea/v2"
)

// handleLSPDiag applies LSP diagnostic markers when the file matches the editor.
func (m *Model) handleLSPDiag(msg LSPDiagnosticsMsg) Model {
	if msg.FilePath == m.editorFilePath {
		m.editor.DiagnosticLines = msg.Lines
		// Count errors (severity 1) and warnings (severity 2) for the statusbar.
		errs, warns := 0, 0
		for _, sev := range msg.Lines {
			switch sev {
			case 1:
				errs++
			case 2:
				warns++
			}
		}
		m.lspErrors = errs
		m.lspWarnings = warns
	}
	return *m
}

// handleGitBranch updates statusbar git state and schedules the next poll.
func (m Model) handleGitBranch(msg gitBranchMsg) (tea.Model, tea.Cmd) {
	m.gitBranch = msg.branch
	m.gitDirty = msg.dirty
	return m, gitBranchTick()
}
