package tui

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
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

type llmHistoryMsg struct{ msg provider.Message }
type llmErrorMsg struct{ err error }

// Streaming delta messages
type llmContentDeltaMsg struct{ content string }
type llmReasoningDeltaMsg struct{ content string }

// ShellOutputMsg carries incremental shell output for real-time display.
// Exported so main.go can send it via program.Send from the shell handler callback.
type ShellOutputMsg struct{ Content string }

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

func (m Model) processLLM() tea.Cmd {
	prov := m.provider
	proxy := m.mcpProxy
	tools := make([]mcp.Tool, len(m.mcpTools))
	copy(tools, m.mcpTools)
	history := make([]provider.Message, len(m.history))
	copy(history, m.history)
	ch := m.updateChan
	ctx := m.turnCtx
	dt := m.deltaTracker
	pad := m.scratchpad

	return func() tea.Msg {
		go func() {
			// Snapshot the project directory before the turn for undo.
			// Capture cwd once so both pre/post snapshots use the same root.
			var preSnap map[string]delta.FileSnapshot
			var snapRoot string
			if dt != nil && dt.TurnID() > 0 {
				if cwd, err := os.Getwd(); err == nil {
					snapRoot = cwd
					preSnap = delta.SnapshotDir(snapRoot)
				}
			}

			start := time.Now()
			var turnIn, turnOut, contextIn int
			err := llm.ProcessTurn(ctx, llm.ProcessTurnOptions{
				Provider:   prov,
				Proxy:      proxy,
				Tools:      tools,
				History:    history,
				Scratchpad: pad,
				// Uses the default from ProcessTurn.
				OnDelta: func(evt provider.StreamEvent) {
					switch evt.Type {
					case provider.EventContentDelta:
						ch <- llmContentDeltaMsg{content: evt.Content}
					case provider.EventReasoningDelta:
						ch <- llmReasoningDeltaMsg{content: evt.Content}
					}
				},
				OnUsage: func(inputTokens, outputTokens int) {
					turnIn += inputTokens
					turnOut += outputTokens
					contextIn = inputTokens
					ch <- llmUsageMsg{inputTokens: inputTokens, outputTokens: outputTokens}
				},
				OnMessage: func(msg provider.Message) {
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
				},
			})

			// Post-turn snapshot: diff against pre to record deltas for undo.
			// Skip on cancellation — partial work has no turnBoundary.
			if preSnap != nil && err == nil {
				postSnap := delta.SnapshotDir(snapRoot)
				delta.RecordDeltas(dt, snapRoot, preSnap, postSnap)
			}

			if err != nil {
				ch <- llmErrorMsg{err: err}
				return
			}
			ch <- llmDoneMsg{
				duration:      time.Since(start),
				timestamp:     start.Format("15:04"),
				inputTokens:   turnIn,
				outputTokens:  turnOut,
				contextTokens: contextIn,
			}
		}()
		return nil
	}
}

// messageToStore converts a provider.Message to a store.SessionMessage.
func messageToStore(msg provider.Message) store.SessionMessage {
	var tc json.RawMessage
	if len(msg.ToolCalls) > 0 {
		tc, _ = json.Marshal(msg.ToolCalls) //nolint:errcheck
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

// saveMessage persists a message to the session store (no-op if store is nil).
func (m *Model) saveMessage(msg provider.Message) {
	if m.store == nil {
		return
	}
	m.store.SaveMessage(m.sessionID, messageToStore(msg))
}
