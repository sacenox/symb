package tui

import (
	tea "charm.land/bubbletea/v2"
)

// handleKeyPress processes key events. Returns (model, cmd, true) if handled.
func (m *Model) handleKeyPress(msg tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	handler := m.keyPressHandlers()[msg.Keystroke()]
	if handler == nil {
		return Model{}, nil, false
	}
	return handler(m)
}

func (m *Model) keyPressHandlers() map[string]func(*Model) (Model, tea.Cmd, bool) {
	return map[string]func(*Model) (Model, tea.Cmd, bool){
		"ctrl+c":       (*Model).handleCtrlC,
		"ctrl+shift+c": (*Model).handleCtrlShiftC,
		"ctrl+shift+v": (*Model).handleCtrlShiftV,
		"ctrl+s":       (*Model).handleCtrlS,
		"esc":          (*Model).handleEsc,
		"enter":        (*Model).handleEnter,
		"ctrl+f":       (*Model).handleCtrlF,
		"ctrl+h":       (*Model).handleCtrlH,
	}
}

func (m *Model) handleCtrlC() (Model, tea.Cmd, bool) {
	return *m, tea.Batch(m.cancelProgramCmd(), m.flushAndQuit()), true
}

func (m *Model) handleCtrlShiftC() (Model, tea.Cmd, bool) {
	if cmd := m.copySelection(); cmd != nil {
		return *m, cmd, true
	}
	return *m, nil, true
}

func (m *Model) handleCtrlShiftV() (Model, tea.Cmd, bool) {
	return *m, tea.ReadClipboard, true
}

func (m *Model) handleCtrlS() (Model, tea.Cmd, bool) {
	if m.turnCancel == nil && !m.turnPending && !m.undoInFlight {
		return *m, m.sendDiffToLLM(), true
	}
	return *m, nil, true
}

func (m *Model) handleEsc() (Model, tea.Cmd, bool) {
	if m.llmInFlight {
		cmd := m.cancelTurnCmd()
		m.cancelTurn()
		return *m, tea.Batch(cmd, m.waitForLLMUpdate()), true
	}
	if m.focus == focusInput {
		m.agentInput.Blur()
	} else {
		m.editor.Blur()
	}
	return *m, nil, true
}

func (m *Model) cancelProgramCmd() tea.Cmd {
	if m.cancel == nil {
		return nil
	}
	cancel := m.cancel
	return func() tea.Msg {
		cancel()
		return nil
	}
}

func (m *Model) handleEnter() (Model, tea.Cmd, bool) {
	if m.focus == focusInput && m.agentInput.Value() != "" && m.turnCancel == nil && !m.turnPending && !m.undoInFlight {
		userMsg := m.agentInput.Value()
		m.agentInput.Reset()
		return *m, m.sendToLLM(userMsg), true
	}
	return Model{}, nil, false
}

func (m *Model) handleCtrlF() (Model, tea.Cmd, bool) {
	if m.searcher != nil {
		m.openFileModal()
		return *m, nil, true
	}
	return *m, nil, false
}

func (m *Model) handleCtrlH() (Model, tea.Cmd, bool) {
	m.openKeybindsModal()
	return *m, nil, true
}

func (m *Model) flushAndQuit() tea.Cmd {
	queue := m.storeQueue
	done := m.storeQueueDone
	return func() tea.Msg {
		if queue != nil {
			close(queue)
			queue = nil
		}
		if done != nil {
			<-done
		}
		return tea.Quit()
	}
}
