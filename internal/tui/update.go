package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/rs/zerolog/log"
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

	case ShellOutputMsg:
		m.ensureStreaming()
		m.streamingContent += msg.Content
		m.rebuildStreamEntries()
		return m, nil

	case undoMsg:
		return m.handleUndo(), nil
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
	userMsg := provider.Message{Role: "user", Content: msg.content, CreatedAt: now}

	historyIdx := len(m.history)
	convIdx := len(m.convEntries)
	m.history = append(m.history, userMsg)

	// Save synchronously to get DB row ID for undo tracking.
	var dbMsgID int64
	if m.store != nil {
		id, err := m.store.SaveMessageSync(m.sessionID, messageToStore(userMsg))
		if err != nil {
			log.Warn().Err(err).Msg("failed to save user message; undo disabled for this turn")
		} else {
			dbMsgID = id
		}
	}

	// Set delta tracker turn so file edits are linked.
	if m.deltaTracker != nil && dbMsgID > 0 {
		m.deltaTracker.BeginTurn(dbMsgID)
	}

	m.turnBoundaries = append(m.turnBoundaries, turnBoundary{
		historyIdx: historyIdx,
		convIdx:    convIdx,
		dbMsgID:    dbMsgID,
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
			m.saveMessage(msg.msg)

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
			// Replace previous turn's undo entry with a normal separator.
			m.demoteOldUndo()
			m.appendText("")
			// Append undo control instead of a plain separator for the latest turn.
			// Store the separator text in full so it can be restored on demote.
			sep := m.makeSeparator(msg.duration.Round(time.Second).String(), msg.timestamp)
			m.appendConv(m.makeUndoEntry(sep))
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
		entry := m.styles.ToolArrow.Render("→") + "  " + m.styles.ToolCall.Render(formatToolCall(tc))
		wasBottom := m.appendText(entry)
		if wasBottom {
			m.scrollOffset = 0
		}
	}
}

// applyToolResultMsg appends tool result display entries.
// It also clears any active streaming state (e.g. from ShellOutputMsg)
// so the next applyAssistantMsg doesn't truncate the tool result entries.
func (m *Model) applyToolResultMsg(msg llmToolResultMsg) {
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

// formatToolCall renders a tool call as Name(key="val", key2="val2").
// Long values are truncated. Falls back to Name(...) on parse errors.
func formatToolCall(tc provider.ToolCall) string {
	const maxVal = 40
	var args map[string]json.RawMessage
	if err := json.Unmarshal(tc.Arguments, &args); err != nil || len(args) == 0 {
		return tc.Name + "(...)"
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		var v interface{}
		if err := json.Unmarshal(args[k], &v); err != nil {
			continue
		}
		s := fmt.Sprintf("%v", v)
		if len(s) > maxVal {
			s = s[:maxVal] + "…"
		}
		parts = append(parts, k+"="+s)
	}
	return tc.Name + "(" + strings.Join(parts, ", ") + ")"
}

// updateComponentSizes pushes layout dimensions to sub-models.
func (m *Model) updateComponentSizes() {
	m.editor.SetWidth(m.layout.editor.Dx())
	m.editor.SetHeight(m.layout.editor.Dy())
	m.agentInput.SetWidth(m.layout.input.Dx() - 2) // padding for border
	m.agentInput.SetHeight(inputRows)
}

// demoteOldUndo finds the existing entryUndo in convEntries and demotes it
// to an entrySeparator (preserving the original display for potential
// re-promotion). Called before appending a new undo entry so only the
// latest turn shows the undo control.
func (m *Model) demoteOldUndo() {
	for i := len(m.convEntries) - 1; i >= 0; i-- {
		if m.convEntries[i].kind == entryUndo {
			m.convEntries[i].kind = entrySeparator
			// full field carries the original separator display text.
			m.convEntries[i].display = m.convEntries[i].full
			m.convLines = nil // invalidate cache
			return
		}
	}
}

// handleUndo reverts the most recent turn: restores files, truncates history
// and convEntries, and cleans up the database.
func (m *Model) handleUndo() Model {
	if m.streaming || len(m.turnBoundaries) == 0 {
		return *m
	}

	tb := m.turnBoundaries[len(m.turnBoundaries)-1]
	m.turnBoundaries = m.turnBoundaries[:len(m.turnBoundaries)-1]

	// 1. Reverse filesystem changes.
	var undoErr error
	var restoredFiles []string
	if m.deltaTracker != nil && tb.dbMsgID > 0 {
		restoredFiles, undoErr = m.deltaTracker.Undo(m.sessionID, tb.dbMsgID)
		m.deltaTracker.DeleteTurn(m.sessionID, tb.dbMsgID)
	}

	// 2. Truncate in-memory history and display.
	m.history = m.history[:tb.historyIdx]
	m.convEntries = m.convEntries[:tb.convIdx]
	m.convLines = nil // invalidate cache

	// 2b. Show error after truncation so it's visible.
	if undoErr != nil {
		m.appendText("", m.styles.Error.Render("undo file restore failed: "+undoErr.Error()), "")
	}

	// 3. Flush async saves then delete messages from DB.
	if m.store != nil && tb.dbMsgID > 0 {
		m.store.Flush()
		if err := m.store.DeleteMessagesFrom(m.sessionID, tb.dbMsgID); err != nil {
			log.Warn().Err(err).Msg("undo: failed to delete messages")
		}
	}

	// 4. Clear file-read tracker (LLM will re-Read as needed).
	if m.fileTracker != nil {
		m.fileTracker.Reset()
	}

	// 5. Update tree-sitter index for restored files only.
	if m.tsIndex != nil {
		for _, f := range restoredFiles {
			m.tsIndex.UpdateFile(f)
		}
	}

	// 6. Promote previous turn's demoted separator back to undo, if any.
	if len(m.turnBoundaries) > 0 {
		for i := len(m.convEntries) - 1; i >= 0; i-- {
			if m.convEntries[i].kind == entrySeparator {
				// full carries the original separator display text.
				m.convEntries[i] = m.makeUndoEntry(m.convEntries[i].full)
				m.convLines = nil
				break
			}
		}
	}

	// 7. Reset streaming state.
	m.streaming = false
	m.streamEntryStart = -1
	m.streamingReasoning = ""
	m.streamingContent = ""

	// 8. Scroll to bottom.
	m.scrollOffset = 0

	return *m
}
