package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/xonecas/symb/internal/provider"
)

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	// -- Window resize -------------------------------------------------------
	case tea.WindowSizeMsg:
		m.handleResize(msg)

	// -- Paste (clipboard read or bracketed paste) ---------------------------
	case tea.ClipboardMsg:
		m.insertPaste(msg.String())
		return m, nil
	case tea.PasteMsg:
		m.insertPaste(msg.Content)
		return m, nil

	// -- Mouse ---------------------------------------------------------------
	case tea.MouseMsg:
		return m.handleMouse(msg)

	// -- Keyboard ------------------------------------------------------------
	case tea.KeyPressMsg:
		if mdl, cmd, handled := m.handleKeyPress(msg); handled {
			return mdl, cmd
		}

	// -- LLM batch (multiple messages drained from updateChan) ---------------
	case llmBatchMsg:
		return m.handleLLMBatch(msg)

	// -- LLM user message (sent before streaming begins) ---------------------
	case llmUserMsg:
		return m.handleUserMsg(msg), tea.Batch(m.processLLM(), m.waitForLLMUpdate())

	case LSPDiagnosticsMsg:
		if msg.FilePath == m.editorFilePath {
			m.editor.DiagnosticLines = msg.Lines
		}
		return m, nil

	case UpdateToolsMsg:
		m.mcpTools = msg.Tools
		return m, nil
	}

	// Always tick spinner
	var cmds []tea.Cmd
	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	cmds = append(cmds, cmd)

	// Forward non-mouse messages to focused component
	if _, isMouse := msg.(tea.MouseMsg); !isMouse {
		m.editor, cmd = m.editor.Update(msg)
		cmds = append(cmds, cmd)
		m.agentInput, cmd = m.agentInput.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

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

// insertPaste inserts pasted text into the focused component.
func (m *Model) insertPaste(text string) {
	if text == "" {
		return
	}
	switch m.focus {
	case focusInput:
		m.agentInput.DeleteSelection()
		m.agentInput.InsertText(text)
	case focusEditor:
		m.editor.DeleteSelection()
		m.editor.InsertText(text)
	}
}

// handleKeyPress processes key events. Returns (model, cmd, true) if handled.
func (m *Model) handleKeyPress(msg tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	switch msg.Keystroke() {
	case "ctrl+c":
		m.cancel()
		return *m, tea.Quit, true
	case "ctrl+shift+c":
		if cmd := m.copySelection(); cmd != nil {
			return *m, cmd, true
		}
		return *m, nil, true
	case "ctrl+shift+v":
		return *m, tea.ReadClipboard, true
	case "esc":
		if m.focus == focusInput {
			m.agentInput.Blur()
		} else {
			m.editor.Blur()
		}
		return *m, nil, true
	case "enter":
		if m.focus == focusInput && m.agentInput.Value() != "" {
			userMsg := m.agentInput.Value()
			m.agentInput.Reset()
			return *m, m.sendToLLM(userMsg), true
		}
	}
	return Model{}, nil, false
}

// handleUserMsg records a user message in history and conversation display.
func (m *Model) handleUserMsg(msg llmUserMsg) Model {
	now := time.Now()
	m.history = append(m.history, provider.Message{
		Role: "user", Content: msg.content, CreatedAt: now,
	})
	m.appendText(styledLines(msg.content, m.styles.Text)...)
	m.appendText("")
	sep := m.makeSeparator("0s", now.Format("15:04:05"))
	wasBottom := m.appendText(sep)
	m.appendText("")
	if wasBottom {
		m.scrollOffset = 0
	}
	return *m
}

// handleLLMBatch processes a batch of messages drained from updateChan.
// Delta messages are accumulated first, with a single rebuildStreamEntries
// at the end, avoiding per-token re-wraps.
func (m Model) handleLLMBatch(batch llmBatchMsg) (tea.Model, tea.Cmd) {
	needRebuild := false

	for _, raw := range batch {
		switch msg := raw.(type) {
		case llmReasoningDeltaMsg:
			m.ensureStreaming()
			m.streamingReasoning += msg.content
			needRebuild = true

		case llmContentDeltaMsg:
			m.ensureStreaming()
			m.streamingContent += msg.content
			needRebuild = true

		case llmHistoryMsg:
			m.history = append(m.history, msg.msg)

		case llmAssistantMsg:
			needRebuild = m.flushRebuild(needRebuild)
			m.applyAssistantMsg(msg)

		case llmToolResultMsg:
			needRebuild = m.flushRebuild(needRebuild)
			m.applyToolResultMsg(msg)

		case llmErrorMsg:
			m.flushRebuild(needRebuild)
			m.appendText("", m.styles.Error.Render("Error: "+msg.err.Error()), "")
			return m, nil

		case llmDoneMsg:
			m.flushRebuild(needRebuild)
			m.appendText("")
			sep := m.makeSeparator(msg.duration.Round(time.Second).String(), msg.timestamp)
			m.appendText(sep)
			return m, nil
		}
	}

	// Apply one rebuild for all accumulated deltas.
	m.flushRebuild(needRebuild)

	// More messages may follow — keep listening.
	return m, m.waitForLLMUpdate()
}

// ensureStreaming initialises stream state on the first delta of a turn.
func (m *Model) ensureStreaming() {
	if m.streaming {
		return
	}
	m.streaming = true
	m.streamEntryStart = len(m.convEntries)
	m.streamWrapStart = len(m.wrappedConvLines())
	m.streamingReasoning = ""
	m.streamingContent = ""
}

// flushRebuild calls rebuildStreamEntries when needed and returns false.
func (m *Model) flushRebuild(needed bool) bool {
	if needed {
		m.rebuildStreamEntries()
	}
	return false
}

// applyAssistantMsg finalizes streaming state and appends the assistant's
// response entries. Extracted so handleLLMBatch can reuse the logic.
func (m *Model) applyAssistantMsg(msg llmAssistantMsg) {
	if m.streaming {
		m.streaming = false
		if m.streamEntryStart >= 0 && m.streamEntryStart <= len(m.convEntries) {
			m.convEntries = m.convEntries[:m.streamEntryStart]
		}
		m.streamEntryStart = -1
		m.streamingReasoning = ""
		m.streamingContent = ""
		m.convLines = nil
	}
	if msg.reasoning != "" {
		wasBottom := m.appendText(styledLines(msg.reasoning, m.styles.Muted)...)
		m.appendText("")
		if wasBottom {
			m.scrollOffset = 0
		}
	}
	if msg.content != "" {
		wasBottom := m.appendText(styledLines(msg.content, m.styles.Text)...)
		m.appendText("")
		if wasBottom {
			m.scrollOffset = 0
		}
	}
	for _, tc := range msg.toolCalls {
		entry := m.styles.ToolArrow.Render("→") + "  " + m.styles.ToolCall.Render(tc.Name+"(...)")
		wasBottom := m.appendText(entry)
		if wasBottom {
			m.scrollOffset = 0
		}
	}
}

// applyToolResultMsg appends tool result display entries.
func (m *Model) applyToolResultMsg(msg llmToolResultMsg) {
	var filePath string
	if sm := toolResultFileRe.FindStringSubmatch(msg.content); sm != nil {
		filePath = sm[1]
	}

	lines := strings.Split(msg.content, "\n")
	preview := lines
	truncated := false
	if len(lines) > maxPreviewLines {
		preview = lines[:maxPreviewLines]
		truncated = true
	}

	arrow := m.styles.ToolArrow.Render("←") + "  "
	var wasBottom bool
	for i, line := range preview {
		display := m.styles.Dim.Render(line)
		if i == 0 {
			display = arrow + display
			wasBottom = m.appendConv(convEntry{display: display, kind: entryToolResult, filePath: filePath, full: msg.content})
		} else {
			m.appendConv(convEntry{display: display, kind: entryToolResult, filePath: filePath, full: msg.content})
		}
	}
	if truncated {
		hint := fmt.Sprintf("  ... %d more lines (click to view)", len(lines)-maxPreviewLines)
		m.appendConv(convEntry{display: m.styles.Muted.Render(hint), kind: entryToolResult, filePath: filePath, full: msg.content})
	}
	if wasBottom {
		m.scrollOffset = 0
	}
}

// updateComponentSizes pushes layout dimensions to sub-models.
func (m *Model) updateComponentSizes() {
	m.editor.SetWidth(m.layout.editor.Dx())
	m.editor.SetHeight(m.layout.editor.Dy())
	m.agentInput.SetWidth(m.layout.input.Dx() - 2) // padding for border
	m.agentInput.SetHeight(inputRows)
}
