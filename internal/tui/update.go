package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
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
	m.frameLines = nil // invalidate per-frame wrap cache

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

	// -- Frame tick (60fps) — rebuild streaming entries for live updates ------
	case tickMsg:
		m.tickStreaming()
		m.tickSpinner(time.Time(msg))
		return m, frameTick()

	// -- LLM batch (multiple messages drained from updateChan) ---------------
	case llmBatchMsg:
		return m.handleLLMBatch(msg)

	// -- LLM user message (sent before streaming begins) ---------------------
	case llmUserMsg:
		m.llmInFlight = true
		return m.handleUserMsg(msg), tea.Batch(m.processLLM(), m.waitForLLMUpdate())

	case LSPDiagnosticsMsg:
		return m.handleLSPDiag(msg), nil
	case UpdateToolsMsg:
		m.mcpTools = msg.Tools
		return m, nil

	case ShellOutputMsg:
		m.ensureStreaming()
		m.streamingContent += msg.Content
		m.streamDirty = true
		return m, nil

	case undoMsg:
		return m.handleUndo(), nil

	case gitBranchMsg:
		return m.handleGitBranch(msg)
	}

	// Forward remaining messages to sub-models (mouse is already handled above).
	return m.forwardToSubModels(msg)
}

// forwardToSubModels sends a non-handled message to sub-editors.
func (m Model) forwardToSubModels(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	var cmd tea.Cmd
	m.editor, cmd = m.editor.Update(msg)
	cmds = append(cmds, cmd)
	m.agentInput, cmd = m.agentInput.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

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
		historyIdx:   historyIdx,
		convIdx:      convIdx,
		dbMsgID:      dbMsgID,
		inputTokens:  m.totalInputTokens,
		outputTokens: m.totalOutputTokens,
	})

	m.appendText(highlightMarkdown(msg.content, m.styles.Text)...)
	m.appendText("")
	sep := m.makeSeparator("0s", now.Format("15:04:05"), 0, 0, 0)
	wasBottom := m.appendText(sep)
	m.appendText("")
	m.turnInputTokens = 0
	m.turnOutputTokens = 0
	if wasBottom {
		m.scrollOffset = 0
	}
	return *m
}

// handleLLMBatch processes a batch of messages drained from updateChan.
// Streaming deltas just mark dirty — the next frame tick does the rebuild.
// Finalization messages (assistant, tool result) flush immediately since
// they clear streaming state.
func (m Model) handleLLMBatch(batch llmBatchMsg) (tea.Model, tea.Cmd) {
	for _, raw := range batch {
		switch msg := raw.(type) {
		case llmReasoningDeltaMsg:
			m.ensureStreaming()
			m.streamingReasoning += msg.content
			m.streamDirty = true

		case llmContentDeltaMsg:
			m.ensureStreaming()
			m.streamingContent += msg.content
			m.streamDirty = true

		case llmHistoryMsg:
			m.history = append(m.history, msg.msg)
			m.saveMessage(msg.msg)

		case llmAssistantMsg:
			m.applyAssistantMsg(msg)

		case llmToolResultMsg:
			m.applyToolResultMsg(msg)

		case llmErrorMsg:
			m.llmInFlight = false
			m.lastNetError = msg.err.Error()
			m.clearStreaming()
			m.appendText("", m.styles.Error.Render("Error: "+msg.err.Error()), "")
			return m, nil

		case llmUsageMsg:
			m.turnInputTokens += msg.inputTokens
			m.turnOutputTokens += msg.outputTokens
			m.totalInputTokens += msg.inputTokens
			m.totalOutputTokens += msg.outputTokens

		case llmDoneMsg:
			m.llmInFlight = false
			m.lastNetError = ""
			m.demoteOldUndo()
			m.appendText("")
			sep := m.makeSeparator(msg.duration.Round(time.Second).String(), msg.timestamp,
				msg.inputTokens, msg.outputTokens, m.totalInputTokens+m.totalOutputTokens)
			m.appendConv(m.makeUndoEntry(sep)...)
			m.trimOldTurns()
			return m, nil
		}
	}

	// More messages may follow — keep listening.
	return m, m.waitForLLMUpdate()
}

// brailleFrames is the spinner animation sequence.
var brailleFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// tickSpinner advances the braille spinner frame based on elapsed time.
// Slow (~500ms/frame) when idle, fast (~100ms/frame) during LLM turns.
func (m *Model) tickSpinner(now time.Time) {
	interval := 500 * time.Millisecond
	if m.llmInFlight {
		interval = 100 * time.Millisecond
	}
	if now.Sub(m.spinFrameAt) >= interval {
		m.spinFrame = (m.spinFrame + 1) % len(brailleFrames)
		m.spinFrameAt = now
	}
}

// tickStreaming rebuilds streaming entries when new content has arrived.
func (m *Model) tickStreaming() {
	if m.streamDirty {
		m.rebuildStreamEntries()
		m.streamDirty = false
	}
}

// ensureStreaming initialises stream state on the first delta of a turn.
func (m *Model) ensureStreaming() {
	if m.streaming {
		return
	}
	m.streaming = true
	m.streamEntryStart = len(m.convEntries)
	m.streamingReasoning = ""
	m.streamingContent = ""
}

// applyAssistantMsg finalizes streaming state and appends the assistant's
// response entries. Extracted so handleLLMBatch can reuse the logic.
func (m *Model) applyAssistantMsg(msg llmAssistantMsg) {
	m.clearStreaming()
	if msg.reasoning != "" {
		wasBottom := m.appendText(styledLines(msg.reasoning, m.styles.Muted)...)
		m.appendText("")
		if wasBottom {
			m.scrollOffset = 0
		}
	}
	if msg.content != "" {
		wasBottom := m.appendText(highlightMarkdown(msg.content, m.styles.Text)...)
		m.appendText("")
		if wasBottom {
			m.scrollOffset = 0
		}
	}
	for _, tc := range msg.toolCalls {
		if m.pendingToolCalls == nil {
			m.pendingToolCalls = make(map[string]provider.ToolCall)
		}
		m.pendingToolCalls[tc.ID] = tc
		entry := m.styles.ToolArrow.Render("→") + "  " + m.styles.ToolCall.Render(formatToolCall(tc))
		wasBottom := m.appendText(entry)
		if wasBottom {
			m.scrollOffset = 0
		}
	}
}

// clearStreaming resets active streaming state.
func (m *Model) clearStreaming() {
	if !m.streaming {
		return
	}
	m.streaming = false
	if m.streamEntryStart >= 0 && m.streamEntryStart <= len(m.convEntries) {
		m.convEntries = m.convEntries[:m.streamEntryStart]
	}
	m.streamEntryStart = -1
	m.streamingReasoning = ""
	m.streamingContent = ""
}

// applyToolResultMsg appends tool result display entries.
// It also clears any active streaming state (e.g. from ShellOutputMsg)
// so the next applyAssistantMsg doesn't truncate the tool result entries.
func (m *Model) applyToolResultMsg(msg llmToolResultMsg) {
	m.clearStreaming()

	var filePath string
	if sm := toolResultFileRe.FindStringSubmatch(msg.content); sm != nil {
		filePath = sm[1]
	}

	// Extract target line for cursor positioning.
	// Read results: from "(lines N-M)". Edit results: from the tool call arguments.
	var startLine int
	if sm := toolResultLineRe.FindStringSubmatch(msg.content); sm != nil {
		startLine, _ = strconv.Atoi(sm[1])
	} else if tc, ok := m.pendingToolCalls[msg.toolCallID]; ok && tc.Name == "Edit" {
		startLine = toolCallEditLine(tc.Arguments)
	}

	// Determine the tool name for display decisions.
	var toolName string
	if tc, ok := m.pendingToolCalls[msg.toolCallID]; ok {
		toolName = tc.Name
	}

	// Grep and Shell show all lines (each clickable); others truncate.
	showFull := toolName == "Grep" || toolName == "Shell"

	// Split diagnostic lines from the main content so they're always shown.
	body, diagLines := extractDiagLines(msg.content)

	lines := strings.Split(body, "\n")
	preview := lines
	truncated := false
	if !showFull && len(lines) > maxPreviewLines {
		preview = lines[:maxPreviewLines]
		truncated = true
	}

	arrow := m.styles.ToolArrow.Render("←") + "  "
	entry := func(display string) convEntry {
		return convEntry{display: display, kind: entryToolResult, filePath: filePath, full: msg.content, line: startLine}
	}
	var wasBottom bool
	for i, line := range preview {
		display := m.styleToolResultLine(line)
		if i == 0 {
			display = arrow + display
			wasBottom = m.appendConv(entry(display))
		} else {
			m.appendConv(entry(display))
		}
	}
	if truncated {
		hint := fmt.Sprintf("  ... %d more lines (click to view)", len(lines)-maxPreviewLines)
		m.appendConv(entry(m.styles.Muted.Render(hint)))
	}
	for _, dl := range diagLines {
		m.appendConv(entry(m.styleToolResultLine(dl)))
	}
	if wasBottom {
		m.scrollOffset = 0
	}
}

// extractDiagLines splits diagnostic lines from tool result content.
// Returns the body without the diagnostics block and the ERROR/WARNING lines.
func extractDiagLines(content string) (body string, diags []string) {
	idx := strings.Index(content, "\nLSP diagnostics:")
	if idx < 0 {
		return content, nil
	}
	for _, dl := range strings.Split(content[idx+1:], "\n") {
		if strings.HasPrefix(dl, "ERROR ") || strings.HasPrefix(dl, "WARNING ") {
			diags = append(diags, dl)
		}
	}
	return content[:idx], diags
}

// styleToolResultLine applies semantic styling to a tool result line.
// Diagnostic lines (ERROR/WARNING) get colored; everything else is dim.
func (m *Model) styleToolResultLine(line string) string {
	switch {
	case strings.HasPrefix(line, "ERROR "):
		return m.styles.Error.Render(line)
	case strings.HasPrefix(line, "WARNING "):
		return m.styles.Warning.Render(line)
	default:
		return m.styles.Dim.Render(line)
	}
}

// toolCallEditLine extracts the target line from an Edit tool call's arguments.
// Returns the start line of the edit operation (replace/insert/delete), or 0.
func toolCallEditLine(args json.RawMessage) int {
	var parsed struct {
		Replace *struct {
			Start struct {
				Line int `json:"line"`
			} `json:"start"`
		} `json:"replace"`
		Insert *struct {
			After struct {
				Line int `json:"line"`
			} `json:"after"`
		} `json:"insert"`
		Delete *struct {
			Start struct {
				Line int `json:"line"`
			} `json:"start"`
		} `json:"delete"`
	}
	if json.Unmarshal(args, &parsed) != nil {
		return 0
	}
	switch {
	case parsed.Replace != nil:
		return parsed.Replace.Start.Line
	case parsed.Insert != nil:
		return parsed.Insert.After.Line
	case parsed.Delete != nil:
		return parsed.Delete.Start.Line
	}
	return 0
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

// demoteOldUndo finds the existing entryUndo in convEntries and removes it.
// The entrySeparator line above it is already present and stays.
// Called before appending a new undo entry so only the latest turn shows the undo control.
func (m *Model) demoteOldUndo() {
	for i := len(m.convEntries) - 1; i >= 0; i-- {
		if m.convEntries[i].kind == entryUndo {
			m.convEntries = append(m.convEntries[:i], m.convEntries[i+1:]...)
			return
		}
	}
}

// trimOldTurns drops the oldest display turns when we exceed maxDisplayTurns.
// Messages are already persisted in the DB — this only trims in-memory state
// to bound rendering cost.
func (m *Model) trimOldTurns() {
	for len(m.turnBoundaries) > maxDisplayTurns {
		// The second boundary marks where the oldest visible turn's display ends.
		cutConv := m.turnBoundaries[1].convIdx
		cutHist := m.turnBoundaries[1].historyIdx

		// Shift convEntries and adjust all boundary indices.
		m.convEntries = m.convEntries[cutConv:]
		m.turnBoundaries = m.turnBoundaries[1:]

		// History: keep the system prompt (index 0) + trim old turn messages.
		// After: history = [system] + history[cutHist:], so what was at
		// cutHist is now at index 1 — offset is cutHist-1.
		histOff := cutHist - 1
		if cutHist > 1 && cutHist < len(m.history) {
			m.history = append(m.history[:1], m.history[cutHist:]...)
		}

		for i := range m.turnBoundaries {
			m.turnBoundaries[i].convIdx -= cutConv
			m.turnBoundaries[i].historyIdx -= histOff
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

	// Restore token totals to the snapshot at turn start.
	m.totalInputTokens = tb.inputTokens
	m.totalOutputTokens = tb.outputTokens
	m.turnInputTokens = 0
	m.turnOutputTokens = 0

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
				// Replace separator with separator+undo pair.
				entries := m.makeUndoEntry(m.convEntries[i].full)
				// Replace the separator entry in-place and insert undo after it.
				m.convEntries[i] = entries[0]
				m.convEntries = append(m.convEntries[:i+1], append([]convEntry{entries[1]}, m.convEntries[i+1:]...)...)
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
