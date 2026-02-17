package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
	"github.com/rs/zerolog/log"
	"github.com/xonecas/symb/internal/delta"
	"github.com/xonecas/symb/internal/llm"
	"github.com/xonecas/symb/internal/mcp"
	"github.com/xonecas/symb/internal/provider"
	"github.com/xonecas/symb/internal/store"
)

// ---------------------------------------------------------------------------
// ELM messages
// ---------------------------------------------------------------------------

type llmUserMsg struct{ content string }

type llmAssistantMsg struct {
	reasoning string
	content   string
	toolCalls []provider.ToolCall
}

type llmToolResultMsg struct {
	toolCallID string
	content    string
}

type llmDoneMsg struct {
	duration      time.Duration
	timestamp     string
	inputTokens   int
	outputTokens  int
	contextTokens int
}

type llmUsageMsg struct {
	inputTokens  int
	outputTokens int
}

type storeBatch struct {
	sessionID string
	msgs      []store.SessionMessage
}

type llmHistoryMsg struct{ msg provider.Message }
type llmErrorMsg struct{ err error }

type userMsgSavedMsg struct {
	convIdx int
	dbMsgID int64
	userMsg provider.Message
	err     error
}

type undoResultMsg struct{ err error }

// Streaming delta messages
type llmContentDeltaMsg struct{ content string }
type llmReasoningDeltaMsg struct{ content string }

// tickMsg drives the 60fps frame loop (~16ms). Rendering work (highlight,
// wrap) is deferred to this tick so streaming deltas don't cause per-batch
// rebuilds.
type tickMsg time.Time

// undoMsg is sent when the user clicks the undo control.
type undoMsg struct{}

// llmBatchMsg carries multiple messages drained from updateChan in one go.
type llmBatchMsg []tea.Msg

// UpdateToolsMsg is exported so main.go can send it via program.Send.
type UpdateToolsMsg struct{ Tools []mcp.Tool }

// LSPDiagnosticsMsg carries diagnostic line severities from the LSP manager to the TUI.
type LSPDiagnosticsMsg struct {
	FilePath string      // absolute path of the file
	Lines    map[int]int // bufRow (0-indexed) -> max severity (1=error, 2=warning)
}

// gitBranchMsg carries the current git branch and dirty status.
type gitBranchMsg struct {
	branch string
	dirty  bool
}

// ---------------------------------------------------------------------------
// ELM commands
// ---------------------------------------------------------------------------

// frameTick returns a command that fires a tickMsg after ~16ms (~60fps).
func frameTick() tea.Cmd {
	return tea.Tick(16*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// gitBranchCmd runs git to detect the current branch and dirty status.
func gitBranchCmd() tea.Cmd {
	return func() tea.Msg {
		branch := ""
		if out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
			branch = strings.TrimSpace(string(out))
		}
		dirty := false
		if err := exec.Command("git", "diff", "--quiet", "HEAD").Run(); err != nil {
			dirty = true // exit code 1 = dirty
		}
		return gitBranchMsg{branch: branch, dirty: dirty}
	}
}

// gitBranchTick schedules a git branch re-poll after a 5-second delay.
func gitBranchTick() tea.Cmd {
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg {
		// Run git commands inline after the delay to avoid an extra message type.
		branch := ""
		if out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
			branch = strings.TrimSpace(string(out))
		}
		dirty := false
		if err := exec.Command("git", "diff", "--quiet", "HEAD").Run(); err != nil {
			dirty = true
		}
		return gitBranchMsg{branch: branch, dirty: dirty}
	})
}

func (m Model) sendToLLM(userInput string) tea.Cmd {
	return func() tea.Msg { return llmUserMsg{content: userInput} }
}

func (m Model) sendDiffToLLM() tea.Cmd {
	return func() tea.Msg {
		if m.editorFilePath == "" {
			return nil
		}
		onDisk, err := os.ReadFile(m.editorFilePath)
		if err != nil {
			return nil
		}
		current := m.editor.Value()
		if current == string(onDisk) {
			return nil
		}
		uri := span.URIFromPath(m.editorFilePath)
		edits := myers.ComputeEdits(uri, string(onDisk), current)
		if len(edits) == 0 {
			return nil
		}
		diff := fmt.Sprint(gotextdiff.ToUnified(m.editorFilePath, m.editorFilePath, string(onDisk), edits))
		if strings.TrimSpace(diff) == "" {
			return nil
		}
		content := "Apply the following diff:\n```diff\n" + diff + "\n```"
		return llmUserMsg{content: content}
	}
}

func (m Model) waitForLLMUpdate() tea.Cmd {
	ch := m.updateChan
	return func() tea.Msg {
		// Block until at least one message arrives.
		first := <-ch
		batch := llmBatchMsg{first}
		// Drain all pending messages without blocking.
		for {
			select {
			case msg := <-ch:
				batch = append(batch, msg)
			default:
				return batch
			}
		}
	}
}

func (m Model) processLLMWithExtra(extra []provider.Message) tea.Cmd {
	deps := m.llmTurnDeps()
	return func() tea.Msg {
		go m.runLLMTurn(deps, extra)
		return nil
	}
}

type llmTurnDeps struct {
	provider  provider.Provider
	proxy     *mcp.Proxy
	tools     []mcp.Tool
	store     *store.Cache
	sessionID string
	ch        chan tea.Msg
	ctx       context.Context
	dt        *delta.Tracker
	pad       llm.ScratchpadReader
	systemMsg *provider.Message
}

type usageTracker struct {
	turnIn    int
	turnOut   int
	contextIn int
}

func (m Model) llmTurnDeps() llmTurnDeps {
	tools := make([]mcp.Tool, len(m.mcpTools))
	copy(tools, m.mcpTools)
	return llmTurnDeps{
		provider:  m.provider,
		proxy:     m.mcpProxy,
		tools:     tools,
		store:     m.store,
		sessionID: m.sessionID,
		ch:        m.updateChan,
		ctx:       m.turnCtx,
		dt:        m.deltaTracker,
		pad:       m.scratchpad,
		systemMsg: m.initialSystemMsg,
	}
}

func (m Model) runLLMTurn(deps llmTurnDeps, extra []provider.Message) {
	history, err := loadHistory(deps.store, deps.sessionID)
	if err != nil {
		deps.ch <- llmErrorMsg{err: fmt.Errorf("load history: %w", err)}
		return
	}
	history = ensureSystemMessage(history, deps.systemMsg)
	if len(extra) > 0 {
		history = append(history, extra...)
	}

	preSnap, snapRoot := snapshotBeforeTurn(deps.dt)

	start := time.Now()
	usage := &usageTracker{}
	err = llm.ProcessTurn(deps.ctx, llm.ProcessTurnOptions{
		Provider:   deps.provider,
		Proxy:      deps.proxy,
		Tools:      deps.tools,
		History:    history,
		Scratchpad: deps.pad,
		OnDelta: func(evt provider.StreamEvent) {
			dispatchStreamEvent(deps.ch, evt)
		},
		OnUsage: usage.onUsage(deps.ch),
		OnMessage: func(msg provider.Message) {
			dispatchHistoryMessage(deps.ch, msg)
		},
	})

	recordTurnDeltas(deps.dt, snapRoot, preSnap, err)
	if err != nil {
		deps.ch <- llmErrorMsg{err: err}
		return
	}
	deps.ch <- llmDoneMsg{
		duration:      time.Since(start),
		timestamp:     start.Format("15:04"),
		inputTokens:   usage.turnIn,
		outputTokens:  usage.turnOut,
		contextTokens: usage.contextIn,
	}
}

func loadHistory(db *store.Cache, sessionID string) ([]provider.Message, error) {
	if db == nil {
		return nil, nil
	}
	stored, err := db.LoadMessages(sessionID)
	if err != nil {
		return nil, err
	}
	return store.ToProviderMessages(stored), nil
}

func ensureSystemMessage(history []provider.Message, systemMsg *provider.Message) []provider.Message {
	if systemMsg == nil {
		return history
	}
	for _, msg := range history {
		if msg.Role == "system" {
			return history
		}
	}
	return append([]provider.Message{*systemMsg}, history...)
}

func snapshotBeforeTurn(dt *delta.Tracker) (map[string]delta.FileSnapshot, string) {
	if dt == nil || dt.TurnID() == 0 {
		return nil, ""
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, ""
	}
	return delta.SnapshotDir(cwd), cwd
}

func recordTurnDeltas(dt *delta.Tracker, snapRoot string, preSnap map[string]delta.FileSnapshot, err error) {
	if preSnap == nil || err != nil {
		return
	}
	postSnap := delta.SnapshotDir(snapRoot)
	delta.RecordDeltas(dt, snapRoot, preSnap, postSnap)
}

func dispatchStreamEvent(ch chan tea.Msg, evt provider.StreamEvent) {
	switch evt.Type {
	case provider.EventContentDelta:
		ch <- llmContentDeltaMsg{content: evt.Content}
	case provider.EventReasoningDelta:
		ch <- llmReasoningDeltaMsg{content: evt.Content}
	}
}

func dispatchHistoryMessage(ch chan tea.Msg, msg provider.Message) {
	ch <- llmHistoryMsg{msg: msg}
	switch msg.Role {
	case roleAssistant:
		ch <- llmAssistantMsg{
			reasoning: msg.Reasoning,
			content:   msg.Content,
			toolCalls: msg.ToolCalls,
		}
	case "tool":
		ch <- llmToolResultMsg{
			toolCallID: msg.ToolCallID,
			content:    msg.Content,
		}
	}
}

func (u *usageTracker) onUsage(ch chan tea.Msg) func(int, int) {
	return func(inputTokens, outputTokens int) {
		u.turnIn += inputTokens
		u.turnOut += outputTokens
		u.contextIn = inputTokens
		ch <- llmUsageMsg{inputTokens: inputTokens, outputTokens: outputTokens}
	}
}

// messageToStore converts a provider.Message to a store.SessionMessage.
func messageToStore(msg provider.Message) store.SessionMessage {
	var tc json.RawMessage
	if len(msg.ToolCalls) > 0 {
		encoded, err := json.Marshal(msg.ToolCalls)
		if err != nil {
			log.Warn().Err(err).Msg("failed to marshal tool calls")
		} else {
			tc = encoded
		}
	}
	return store.SessionMessage{
		Role:         msg.Role,
		Content:      msg.Content,
		Reasoning:    msg.Reasoning,
		ToolCalls:    tc,
		ToolCallID:   msg.ToolCallID,
		CreatedAt:    msg.CreatedAt,
		InputTokens:  msg.InputTokens,
		OutputTokens: msg.OutputTokens,
	}
}

func (m *Model) saveMessages(msgs []provider.Message) {
	if m.store == nil || len(msgs) == 0 {
		return
	}
	stored := make([]store.SessionMessage, 0, len(msgs))
	for _, msg := range msgs {
		stored = append(stored, messageToStore(msg))
	}
	if enqueueStoreBatch(m.storeQueue, storeBatch{sessionID: m.sessionID, msgs: stored}) {
		return
	}
	if err := m.store.SaveMessages(m.sessionID, stored); err != nil {
		log.Warn().Err(err).Msg("failed to save message batch")
	}
}

func (m Model) saveMessagesCmd(msgs []provider.Message) tea.Cmd {
	if m.store == nil || len(msgs) == 0 {
		return nil
	}
	return func() tea.Msg {
		m.saveMessages(msgs)
		return nil
	}
}

func enqueueStoreBatch(queue chan storeBatch, batch storeBatch) (ok bool) {
	if queue == nil {
		return false
	}
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	select {
	case queue <- batch:
		return true
	default:
		log.Warn().Msg("store queue full; dropping message batch")
		return false
	}
}

func startStoreWorker(db *store.Cache, queue <-chan storeBatch) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for batch := range queue {
			for attempt := 0; attempt <= store.SQLiteBusyMaxRetries; attempt++ {
				err := db.SaveMessages(batch.sessionID, batch.msgs)
				if err == nil {
					break
				}
				if !store.IsSQLiteBusy(err) || attempt == store.SQLiteBusyMaxRetries {
					log.Warn().Err(err).Msg("failed to save message batch")
					break
				}
				log.Warn().Err(err).Msg("retrying message save after busy")
				backoff := time.Duration((attempt+1)*store.SQLiteBusyBackoffStepMs) * time.Millisecond
				if backoff > store.SQLiteBusyMaxBackoff {
					backoff = store.SQLiteBusyMaxBackoff
				}
				time.Sleep(backoff)
			}
		}
	}()
	return done
}
