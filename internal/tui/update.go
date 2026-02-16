package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/rs/zerolog/log"
	"github.com/xonecas/symb/internal/filesearch"
	"github.com/xonecas/symb/internal/provider"
	"github.com/xonecas/symb/internal/tui/modal"
)

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.frameLines = nil // invalidate per-frame wrap cache

	// File finder modal intercepts all input when open.
	if mdl, cmd, handled := m.updateFileModal(msg); handled {
		return mdl, cmd
	}

	switch msg := msg.(type) {

	// -- Window resize -------------------------------------------------------
	case tea.WindowSizeMsg:
		m.handleResize(msg)

	// -- Paste (clipboard read or bracketed paste) ---------------------------
	case tea.ClipboardMsg, tea.PasteMsg:
		return m.handlePaste(msg)

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
		return m.handleLLMUser(msg)

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

func (m Model) handlePaste(msg tea.Msg) (tea.Model, tea.Cmd) {
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
	return m, nil
}

func (m Model) handleLLMUser(msg llmUserMsg) (tea.Model, tea.Cmd) {
	m = m.handleUserMsg(msg)
	if m.provider == nil {
		return m, nil
	}
	m.llmInFlight = true
	m.turnCtx, m.turnCancel = context.WithCancel(context.Background())
	return m, tea.Batch(m.processLLM(), m.waitForLLMUpdate())
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
		if m.llmInFlight {
			m.cancelTurn()
			return *m, m.waitForLLMUpdate(), true
		}
		if m.focus == focusInput {
			m.agentInput.Blur()
		} else {
			m.editor.Blur()
		}
		return *m, nil, true
	case "enter":
		if m.focus == focusInput && m.agentInput.Value() != "" && m.turnCancel == nil {
			userMsg := m.agentInput.Value()
			m.agentInput.Reset()
			return *m, m.sendToLLM(userMsg), true
		}
	case "ctrl+f":
		if m.searcher != nil {
			m.openFileModal()
			return *m, nil, true
		}
	}
	return Model{}, nil, false
}

// handleUserMsg records a user message in the DB and conversation display.
func (m *Model) handleUserMsg(msg llmUserMsg) Model {
	now := time.Now()
	userMsg := provider.Message{Role: "user", Content: msg.content, CreatedAt: now}

	convIdx := len(m.convEntries)

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
		convIdx:      convIdx,
		dbMsgID:      dbMsgID,
		inputTokens:  m.totalInputTokens,
		outputTokens: m.totalOutputTokens,
	})

	m.appendText(highlightMarkdown(msg.content, m.styles.Text)...)
	m.appendText("")
	sep := m.makeSeparator("0s", now.Format("15:04:05"), 0, 0, 0, 0)
	wasBottom := m.appendText(sep)
	m.appendText("")
	m.turnInputTokens = 0
	m.turnOutputTokens = 0
	m.turnContextTokens = 0
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
	if !m.llmInFlight {
		return m.drainCancelled(batch)
	}
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
			m.saveMessage(msg.msg)

		case llmAssistantMsg:
			m.applyAssistantMsg(msg)

		case llmToolResultMsg:
			m.applyToolResultMsg(msg)

		case llmErrorMsg:
			m.finishTurn()
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
			m.finishTurn()
			m.lastNetError = ""
			m.demoteOldUndo()
			m.appendText("")
			m.turnContextTokens = msg.contextTokens
			sep := m.makeSeparator(msg.duration.Round(time.Second).String(), msg.timestamp,
				msg.inputTokens, msg.outputTokens, m.totalInputTokens+m.totalOutputTokens, m.turnContextTokens)
			m.appendConv(m.makeUndoEntry(sep)...)
			m.trimOldTurns()
			return m, nil
		}
	}

	// More messages may follow — keep listening.
	return m, m.waitForLLMUpdate()
}

// drainCancelled handles a batch after the turn was cancelled.
// It persists history messages and patches invalid API state on termination.
func (m Model) drainCancelled(batch llmBatchMsg) (tea.Model, tea.Cmd) {
	for _, raw := range batch {
		switch msg := raw.(type) {
		case llmHistoryMsg:
			m.saveMessage(msg.msg)
		case llmDoneMsg, llmErrorMsg:
			m.patchInterruptedHistory()
			m.turnCancel = nil
			m.turnCtx = nil
			return m, nil
		}
	}
	return m, m.waitForLLMUpdate()
}

// finishTurn clears in-flight state and cancels the turn context.
func (m *Model) finishTurn() {
	m.llmInFlight = false
	if m.turnCancel != nil {
		m.turnCancel()
		m.turnCancel = nil
	}
	m.turnCtx = nil
}

// patchInterruptedHistory appends a synthetic assistant message if the
// interrupted turn left history in an invalid API state (trailing tool_use
// without result, or trailing tool result with no assistant follow-up).
func (m *Model) patchInterruptedHistory() {
	if m.store == nil {
		return
	}
	last, err := m.store.LoadLastMessage(m.sessionID)
	if err != nil || last == nil {
		return
	}
	hasToolCalls := len(last.ToolCalls) > 0 && string(last.ToolCalls) != "[]"
	needsPatch := last.Role != roleAssistant || hasToolCalls
	if needsPatch {
		interruptMsg := provider.Message{
			Role:      roleAssistant,
			Content:   "The user interrupted me.",
			CreatedAt: time.Now(),
		}
		m.saveMessage(interruptMsg)
	}
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

// cancelTurn gracefully interrupts the in-flight LLM turn.
// History, turn boundaries, file changes, and deltas are preserved
// so the user can undo via the message footer if desired.
// turnCancel is kept non-nil until the drain loop finishes (see
// handleLLMBatch) so the enter guard blocks new submissions.
func (m *Model) cancelTurn() {
	if m.turnCancel != nil {
		m.turnCancel()
	}
	m.llmInFlight = false
	m.clearStreaming()
	m.appendText("", m.styles.Dim.Render("(interrupted)"), "")
	m.scrollOffset = 0
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

	body, diagLines := extractDiagLines(msg.content)
	if idx := strings.Index(body, "\n"); idx >= 0 {
		body = body[:idx]
	}

	arrow := m.styles.ToolArrow.Render("←") + "  "
	entry := convEntry{display: arrow + m.styleToolResultLine(body), kind: entryToolResult, filePath: filePath, full: msg.content, line: startLine}
	wasBottom := m.appendConv(entry)
	for _, dl := range diagLines {
		m.appendConv(convEntry{display: m.styleToolResultLine(dl), kind: entryToolResult, filePath: filePath, full: msg.content, line: startLine})
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
// Messages live in the DB — this only trims display entries to bound rendering cost.
func (m *Model) trimOldTurns() {
	for len(m.turnBoundaries) > maxDisplayTurns {
		cutConv := m.turnBoundaries[1].convIdx

		m.convEntries = m.convEntries[cutConv:]
		m.turnBoundaries = m.turnBoundaries[1:]

		for i := range m.turnBoundaries {
			m.turnBoundaries[i].convIdx -= cutConv
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

	// 2. Truncate display entries.
	m.convEntries = m.convEntries[:tb.convIdx]

	// 2b. Show error after truncation so it's visible.
	if undoErr != nil {
		m.appendText("", m.styles.Error.Render("undo file restore failed: "+undoErr.Error()), "")
	}

	// 3. Delete messages from DB.
	if m.store != nil && tb.dbMsgID > 0 {
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

func (m *Model) openFileModal() {
	searchFn := func(query string) []modal.Item {
		if query == "" {
			return nil
		}
		results, err := m.searcher.Search(context.Background(), filesearch.Options{
			Pattern:       query,
			ContentSearch: false,
			MaxResults:    50,
		})
		if err != nil {
			return nil
		}
		items := make([]modal.Item, len(results))
		for i, r := range results {
			items[i] = modal.Item{Name: r.Path}
		}
		return items
	}
	md := modal.New(searchFn, "Open: ", modal.Colors{
		Fg:     palette.Fg,
		Bg:     palette.Bg,
		Dim:    palette.Dim,
		SelFg:  palette.Fg,
		SelBg:  palette.Bg,
		Border: palette.Border,
	})
	m.fileModal = &md
}

func (m *Model) updateFileModal(msg tea.Msg) (Model, tea.Cmd, bool) {
	if m.fileModal == nil {
		return *m, nil, false
	}
	action, cmd := m.fileModal.HandleMsg(msg)
	switch a := action.(type) {
	case modal.ActionClose:
		m.fileModal = nil
		return *m, nil, true
	case modal.ActionSelect:
		m.fileModal = nil
		openCmd := m.openFile(a.Item.Name, 0)
		return *m, openCmd, true
	}
	if cmd != nil {
		return *m, cmd, true
	}
	// Modal is open — absorb key/mouse events, pass through others.
	switch msg.(type) {
	case tea.KeyPressMsg, tea.MouseMsg:
		return *m, nil, true
	}
	return *m, nil, false
}
