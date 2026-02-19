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
	"github.com/xonecas/symb/internal/hashline"
	"github.com/xonecas/symb/internal/provider"
	"github.com/xonecas/symb/internal/store"
)

func (m Model) handleLLMUser(msg llmUserMsg) (tea.Model, tea.Cmd) {
	updated, cmd := m.handleUserMsg(msg)
	return updated, cmd
}

// handleUserMsg records a user message in the conversation display.
func (m *Model) handleUserMsg(msg llmUserMsg) (Model, tea.Cmd) {
	now := time.Now()
	llmMsg := provider.Message{Role: "user", Content: msg.content, CreatedAt: now}
	storeMsg := provider.Message{Role: "user", Content: msg.display, CreatedAt: now}

	convIdx := len(m.convEntries)

	if m.provider != nil {
		m.turnPending = true
	}

	m.turnBoundaries = append(m.turnBoundaries, turnBoundary{
		convIdx:      convIdx,
		dbMsgID:      0,
		inputTokens:  m.totalInputTokens,
		outputTokens: m.totalOutputTokens,
	})

	m.appendText("")
	m.appendText(highlightMarkdown(msg.display, m.styles.Text)...)
	wasBottom := m.appendText("")
	m.turnInputTokens = 0
	m.turnOutputTokens = 0
	m.turnContextTokens = 0
	if wasBottom {
		m.scrollOffset = 0
	}
	cmd := m.saveUserMessageCmd(llmMsg, storeMsg, convIdx)
	return *m, cmd
}

func (m *Model) saveUserMessageCmd(llmMsg, storeMsg provider.Message, convIdx int) tea.Cmd {
	store := m.store
	sessionID := m.sessionID
	if store == nil {
		return func() tea.Msg {
			return userMsgSavedMsg{convIdx: convIdx, dbMsgID: 0, userMsg: llmMsg}
		}
	}
	return func() tea.Msg {
		id, err := store.SaveMessageSync(sessionID, messageToStore(storeMsg))
		if err != nil {
			return userMsgSavedMsg{convIdx: convIdx, dbMsgID: 0, userMsg: llmMsg, err: err}
		}
		return userMsgSavedMsg{convIdx: convIdx, dbMsgID: id, userMsg: llmMsg}
	}
}

func (m Model) handleUserMsgSaved(msg userMsgSavedMsg) (tea.Model, tea.Cmd) {
	m.turnPending = false
	for i := range m.turnBoundaries {
		if m.turnBoundaries[i].convIdx == msg.convIdx {
			m.turnBoundaries[i].dbMsgID = msg.dbMsgID
			break
		}
	}
	if m.deltaTracker != nil && msg.dbMsgID > 0 {
		m.deltaTracker.BeginTurn(msg.dbMsgID)
	}
	if m.provider == nil {
		return m, nil
	}
	m.llmInFlight = true
	m.turnCtx, m.turnCancel = context.WithCancel(context.Background())
	// Always supply the current user message via extra so the LLM receives the
	// expanded form (@ mentions replaced with file content). When the store is
	// present the display form was saved to DB; we need to exclude it from the
	// loaded history to avoid sending a duplicate.
	extra := []provider.Message{msg.userMsg}
	return m, tea.Batch(m.processLLMWithExtra(extra), m.waitForLLMUpdate())
}

// handleLLMBatch processes a batch of messages drained from updateChan.
// Streaming deltas just mark dirty — the next frame tick does the rebuild.
// Finalization messages (assistant, tool result) flush immediately since
// they clear streaming state.
func (m Model) handleLLMBatch(batch llmBatchMsg) (tea.Model, tea.Cmd) {
	var history []provider.Message
	for _, raw := range batch {
		if msg, ok := raw.(llmHistoryMsg); ok {
			history = append(history, msg.msg)
		}
	}
	saveCmd := m.saveMessagesCmd(history)
	if !m.llmInFlight {
		return m.drainCancelled(batch, saveCmd)
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
			// Saved in a single transaction above.

		case llmAssistantMsg:
			m.applyAssistantMsg(msg)

		case llmToolResultMsg:
			m.applyToolResultMsg(msg)

		case llmErrorMsg:
			m.finishTurn()
			m.lastNetError = msg.err.Error()
			m.clearStreaming()
			m.appendText("", m.styles.Error.Render("Error: "+msg.err.Error()), "")
			return m, saveCmd

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
			sep := makeSeparator(m.styles, msg.duration.Round(time.Second).String(), msg.timestamp,
				msg.inputTokens, msg.outputTokens, m.totalInputTokens+m.totalOutputTokens, m.turnContextTokens)
			m.appendConv(m.makeUndoEntry(sep)...)
			m.trimOldTurns()
			return m, saveCmd
		}
	}

	// More messages may follow — keep listening.
	return m, tea.Batch(saveCmd, m.waitForLLMUpdate())
}

// drainCancelled handles a batch after the turn was cancelled.
// It persists history messages and patches invalid API state on termination.
func (m Model) drainCancelled(batch llmBatchMsg, saveCmd tea.Cmd) (tea.Model, tea.Cmd) {
	for _, raw := range batch {
		switch raw.(type) {
		case llmHistoryMsg:
			// Saved in a single transaction above.
		case llmDoneMsg, llmErrorMsg:
			m.turnCancel = nil
			m.turnCtx = nil
			return m, tea.Batch(saveCmd, m.patchInterruptedHistoryCmd())
		}
	}
	return m, tea.Batch(saveCmd, m.waitForLLMUpdate())
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
func (m *Model) patchInterruptedHistoryCmd() tea.Cmd {
	if m.store == nil {
		return nil
	}
	cache := m.store
	sessionID := m.sessionID
	storeQueue := m.storeQueue
	return func() tea.Msg {
		last, err := cache.LoadLastMessage(sessionID)
		if err != nil || last == nil {
			return nil
		}
		hasToolCalls := len(last.ToolCalls) > 0 && string(last.ToolCalls) != "[]"
		needsPatch := last.Role != roleAssistant || hasToolCalls
		if needsPatch {
			interruptMsg := provider.Message{
				Role:      roleAssistant,
				Content:   "The user interrupted me.",
				CreatedAt: time.Now(),
			}
			stored := []store.SessionMessage{messageToStore(interruptMsg)}
			if enqueueStoreBatch(storeQueue, storeBatch{sessionID: sessionID, msgs: stored}) {
				return nil
			}
			if err := cache.SaveMessages(sessionID, stored); err != nil {
				return nil
			}
		}
		return nil
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
		display := m.styles.ToolArrow.Render("→") + m.styles.BgFill.Render("  ") + m.styles.ToolCall.Render(formatToolCall(tc))
		wasBottom := m.appendConv(convEntry{display: display, kind: entryToolCall})
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
	m.llmInFlight = false
	m.clearStreaming()
	m.appendText("", m.styles.Dim.Render("(interrupted)"), "")
	m.scrollOffset = 0
}

func (m *Model) cancelTurnCmd() tea.Cmd {
	if m.turnCancel == nil {
		return nil
	}
	cancel := m.turnCancel
	return func() tea.Msg {
		cancel()
		return nil
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
// It also clears any active streaming state so the next applyAssistantMsg doesn't truncate the tool result entries.
func (m *Model) applyToolResultMsg(msg llmToolResultMsg) {
	m.clearStreaming()

	var filePath string
	if sm := toolResultFileRe.FindStringSubmatch(msg.content); sm != nil {
		filePath = sm[1]
	}

	// Resolve the tool name from the pending call.
	var toolName string
	if tc, ok := m.pendingToolCalls[msg.toolCallID]; ok {
		toolName = tc.Name
	}

	// Extract target line for cursor positioning.
	// Read results: from "(lines N-M)". Edit results: from the tool call arguments.
	var startLine int
	if sm := toolResultLineRe.FindStringSubmatch(msg.content); sm != nil {
		startLine, _ = strconv.Atoi(sm[1])
	} else if tc, ok := m.pendingToolCalls[msg.toolCallID]; ok && tc.Name == "Edit" {
		startLine = toolCallEditLine(tc.Arguments)
	}
	if startLine == 0 && filePath != "" {
		startLine = toolResultHashlineStart(msg.content, filePath)
	}

	body, diagLines := extractDiagLines(msg.content)
	if idx := strings.Index(body, "\n"); idx >= 0 {
		body = body[:idx]
	}

	// Build display: "← summary  [view]"
	arrow := m.styles.ToolArrow.Render("←") + m.styles.BgFill.Render("  ")
	summary := arrow + m.styleToolResultLine(body)
	viewBtn := m.styles.BgFill.Render("  ") + m.styles.Clickable.Render("view")
	display := summary + viewBtn

	entry := convEntry{
		display:  display,
		kind:     entryToolResult,
		filePath: filePath,
		full:     msg.content,
		line:     startLine,
		toolName: toolName,
	}
	wasBottom := m.appendConv(entry)
	for _, dl := range diagLines {
		m.appendConv(convEntry{display: m.styleToolResultLine(dl), kind: entryToolDiag, full: msg.content})
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

// toolResultHashlineStart scans a tool result body for the first hashline and
// returns its line number if it matches the provided file path.
func toolResultHashlineStart(content, filePath string) int {
	prefix := "Read " + filePath + " "
	if !strings.HasPrefix(content, prefix) {
		prefix = "Edited " + filePath + " "
		if !strings.HasPrefix(content, prefix) {
			prefix = "Created " + filePath + " "
			if !strings.HasPrefix(content, prefix) {
				return 0
			}
		}
	}
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		idx := strings.IndexByte(line, '|')
		if idx <= 0 {
			continue
		}
		anchor := line[:idx]
		parsed, err := hashline.ParseAnchor(anchor)
		if err != nil {
			continue
		}
		return parsed.Num
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
