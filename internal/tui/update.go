package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {

	m.frameLines = nil // invalidate per-frame wrap cache

	if mdl, cmd, handled := m.handleModalMsg(msg); handled {
		return mdl, cmd
	}
	if mdl, cmd, handled := m.handleUIEvent(msg); handled {
		return mdl, cmd
	}
	if mdl, cmd, handled := m.handleLLMEvent(msg); handled {
		return mdl, cmd
	}
	if mdl, cmd, handled := m.handleSystemEvent(msg); handled {
		return mdl, cmd
	}

	// Forward remaining messages to sub-models (mouse is already handled above).
	return m.forwardToSubModels(msg)
}

func (m Model) handleModalMsg(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	// Keybinds modal intercepts all input when open.
	if mdl, cmd, handled := m.updateKeybindsModal(msg); handled {
		return mdl, cmd, true
	}
	// File finder modal intercepts all input when open.
	if mdl, cmd, handled := m.updateFileModal(msg); handled {
		return mdl, cmd, true
	}
	// Models modal intercepts all input when open.
	if mdl, cmd, handled := m.updateModelsModal(msg); handled {
		return mdl, cmd, true
	}
	// Tool viewer modal intercepts all input when open.
	if mdl, cmd, handled := m.updateToolViewModal(msg); handled {
		return mdl, cmd, true
	}
	return m, nil, false
}

func (m Model) handleUIEvent(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.handleResize(msg)
		return m, nil, true
	case tea.ClipboardMsg, tea.PasteMsg:
		mdl := m.handlePaste(msg)
		return mdl, nil, true
	case tea.MouseMsg:
		mdl, cmd := m.handleMouse(msg)
		return mdl, cmd, true
	case tea.KeyPressMsg:
		if mdl, cmd, handled := m.handleKeyPress(msg); handled {
			return mdl, cmd, true
		}
		return m, nil, false
	case tickMsg:
		m.tickStreaming()
		m.tickSpinner(time.Time(msg))
		return m, frameTick(), true
	}
	return m, nil, false
}

func (m Model) handleLLMEvent(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case llmBatchMsg:
		mdl, cmd := m.handleLLMBatch(msg)
		return mdl, cmd, true
	case llmUserMsg:
		mdl, cmd := m.handleLLMUser(msg)
		return mdl, cmd, true
	case userMsgSavedMsg:
		mdl, cmd := m.handleUserMsgSaved(msg)
		return mdl, cmd, true
	}
	return m, nil, false
}

func (m Model) handleSystemEvent(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case LSPDiagnosticsMsg:
		return m.handleLSPDiag(msg), nil, true
	case UpdateToolsMsg:
		m.mcpTools = msg.Tools
		return m, nil, true
	case undoMsg:
		mdl, cmd := m.handleUndo()
		return mdl, cmd, true
	case openToolViewMsg:
		m.openToolViewModal(msg.title, msg.content)
		return m, nil, true
	case undoResultMsg:
		return m.handleUndoResult(msg), nil, true
	case gitBranchMsg:
		mdl, cmd := m.handleGitBranch(msg)
		return mdl, cmd, true
	case modelsFetchedMsg:
		return m.handleModelsFetched(msg), nil, true
	case modelSwitchedMsg:
		return m.handleModelSwitched(msg), nil, true
	}
	return m, nil, false
}

// forwardToSubModels sends a non-handled message to sub-models.
func (m Model) forwardToSubModels(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.agentInput, cmd = m.agentInput.Update(msg)
	return m, cmd
}

func (m Model) handlePaste(msg tea.Msg) tea.Model {
	var text string
	switch v := msg.(type) {
	case tea.ClipboardMsg:
		text = v.Content
	case tea.PasteMsg:
		text = v.Content
	}
	if text != "" {
		m.insertPaste(text)
	}
	return m
}

// insertPaste inserts pasted text into the agent input.
func (m *Model) insertPaste(text string) {
	if text == "" {
		return
	}
	m.agentInput.DeleteSelection()
	m.agentInput.InsertText(text)
}
