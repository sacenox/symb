package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/xonecas/symb/internal/llm"
	"github.com/xonecas/symb/internal/mcp"
	"github.com/xonecas/symb/internal/provider"
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
	duration  time.Duration
	timestamp string
}

type llmHistoryMsg struct{ msg provider.Message }
type llmErrorMsg struct{ err error }

// Streaming delta messages
type llmContentDeltaMsg struct{ content string }
type llmReasoningDeltaMsg struct{ content string }

// UpdateToolsMsg is exported so main.go can send it via program.Send.
type UpdateToolsMsg struct{ Tools []mcp.Tool }

// LSPDiagnosticsMsg carries diagnostic line severities from the LSP manager to the TUI.
type LSPDiagnosticsMsg struct {
	FilePath string      // absolute path of the file
	Lines    map[int]int // bufRow (0-indexed) -> max severity (1=error, 2=warning)
}

// ---------------------------------------------------------------------------
// ELM commands
// ---------------------------------------------------------------------------

func (m Model) sendToLLM(userInput string) tea.Cmd {
	return func() tea.Msg { return llmUserMsg{content: userInput} }
}

func (m Model) waitForLLMUpdate() tea.Cmd {
	return func() tea.Msg { return <-m.updateChan }
}

func (m Model) processLLM() tea.Cmd {
	prov := m.provider
	proxy := m.mcpProxy
	tools := make([]mcp.Tool, len(m.mcpTools))
	copy(tools, m.mcpTools)
	history := make([]provider.Message, len(m.history))
	copy(history, m.history)
	ch := m.updateChan
	ctx := m.ctx

	return func() tea.Msg {
		go func() {
			start := time.Now()
			err := llm.ProcessTurn(ctx, llm.ProcessTurnOptions{
				Provider:      prov,
				Proxy:         proxy,
				Tools:         tools,
				History:       history,
				MaxToolRounds: 20,
				OnDelta: func(evt provider.StreamEvent) {
					switch evt.Type {
					case provider.EventContentDelta:
						ch <- llmContentDeltaMsg{content: evt.Content}
					case provider.EventReasoningDelta:
						ch <- llmReasoningDeltaMsg{content: evt.Content}
					}
				},
				OnMessage: func(msg provider.Message) {
					ch <- llmHistoryMsg{msg: msg}
					switch msg.Role {
					case "assistant":
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
			if err != nil {
				ch <- llmErrorMsg{err: err}
				return
			}
			ch <- llmDoneMsg{
				duration:  time.Since(start),
				timestamp: start.Format("15:04"),
			}
		}()
		return nil
	}
}
