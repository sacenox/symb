// Package llm implements the LLM interaction loop with tool calling support.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/xonecas/symb/internal/mcp"
	"github.com/xonecas/symb/internal/provider"
)

// MessageCallback is called when a complete message should be added to history.
type MessageCallback func(msg provider.Message)

// DeltaCallback is called for each streaming event (content/reasoning deltas).
type DeltaCallback func(evt provider.StreamEvent)

// ToolCallCallback is called when tool calls are about to be executed.
type ToolCallCallback func()

// UsageCallback is called with accumulated token usage after each LLM call.
type UsageCallback func(inputTokens, outputTokens int)

// ScratchpadReader provides read access to the agent's working plan.
type ScratchpadReader interface {
	Content() string
}

// ProcessTurnOptions holds configuration for processing a turn.
type ProcessTurnOptions struct {
	Provider      provider.Provider
	Proxy         *mcp.Proxy
	Tools         []mcp.Tool
	History       []provider.Message
	OnMessage     MessageCallback
	OnDelta       DeltaCallback    // Optional: called for each stream event
	OnToolCall    ToolCallCallback // Optional: called before executing tool calls
	OnUsage       UsageCallback    // Optional: called with token usage after each LLM call
	Scratchpad    ScratchpadReader // Optional: agent plan injected at context tail
	MaxToolRounds int
}

// streamAndCollect runs one LLM call: streams events, collects the response,
// reports usage, and returns the ChatResponse.
func streamAndCollect(ctx context.Context, opts *ProcessTurnOptions, tools []provider.Tool) (*provider.ChatResponse, error) {
	stream, err := opts.Provider.ChatStream(ctx, opts.History, tools)
	if err != nil {
		return nil, err
	}
	resp, err := collectWithDeltas(stream, opts.OnDelta)
	if err != nil {
		return nil, err
	}
	if opts.OnUsage != nil && (resp.InputTokens > 0 || resp.OutputTokens > 0) {
		opts.OnUsage(resp.InputTokens, resp.OutputTokens)
	}
	return resp, nil
}

// emitAssistant builds an assistant message from a ChatResponse, emits it, and appends to history.
func emitAssistant(opts *ProcessTurnOptions, resp *provider.ChatResponse) {
	msg := provider.Message{
		Role:         "assistant",
		Content:      resp.Content,
		Reasoning:    resp.Reasoning,
		ToolCalls:    resp.ToolCalls,
		CreatedAt:    time.Now(),
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
	}
	if opts.OnMessage != nil {
		opts.OnMessage(msg)
	}
	opts.History = append(opts.History, msg)
}

// ProcessTurn handles one conversation turn, which may involve tool calls.
// It streams events via OnDelta and emits complete messages via OnMessage.
func ProcessTurn(ctx context.Context, opts ProcessTurnOptions) error {
	if opts.MaxToolRounds == 0 {
		opts.MaxToolRounds = 40
	}

	// Convert MCP tools to provider format once
	providerTools := make([]provider.Tool, len(opts.Tools))
	for i, t := range opts.Tools {
		providerTools[i] = provider.Tool{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.InputSchema,
		}
	}

	for round := 0; round < opts.MaxToolRounds; round++ {
		// After round 0 the history contains tool results that the LLM
		// has already responded to. Compact them so we don't resend
		// large payloads on every subsequent call.
		if round > 0 {
			compactOldToolResults(opts.History)
		}

		// Inject context recitation at the tail of the history to keep
		// the model focused. Two sources:
		// 1. Scratchpad (agent-written plan) — always injected when present.
		// 2. Goal reminder (user's original request) — injected every
		//    reminderInterval rounds as a fallback when no scratchpad exists.
		opts.History = injectRecitation(opts.History, opts.Scratchpad, round)

		resp, err := streamAndCollect(ctx, &opts, providerTools)
		if err != nil {
			return fmt.Errorf("LLM stream failed: %w", err)
		}

		emitAssistant(&opts, resp)

		// If no tool calls, we're done
		if len(resp.ToolCalls) == 0 {
			return nil
		}

		// Notify about tool calls if callback provided
		if opts.OnToolCall != nil {
			opts.OnToolCall()
		}

		// Execute each tool call and update history
		toolResults := executeToolCalls(ctx, opts.Proxy, resp.ToolCalls, opts.OnMessage)
		opts.History = append(opts.History, toolResults...)

		// Continue loop to let LLM process tool results
	}

	// Tool call limit reached — do one final call with no tools so the LLM
	// must reply with text summarizing progress.
	if err := ctx.Err(); err != nil {
		return err
	}

	limitMsg := provider.Message{
		Role:      "user",
		Content:   "You have exhausted your tool call limit for this turn. Respond in text only. Summarize what you accomplished and what remains.",
		CreatedAt: time.Now(),
	}
	if opts.OnMessage != nil {
		opts.OnMessage(limitMsg)
	}
	opts.History = append(opts.History, limitMsg)

	resp, err := streamAndCollect(ctx, &opts, nil)
	if err != nil {
		return fmt.Errorf("final text-only LLM stream failed: %w", err)
	}

	emitAssistant(&opts, resp)
	return nil
}

// toolCallAccumulator tracks tool calls as they stream in.
type toolCallAccumulator struct {
	byIndex     map[int]int
	calls       []provider.ToolCall
	argBuilders []string
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{byIndex: make(map[int]int)}
}

func (a *toolCallAccumulator) begin(evt provider.StreamEvent) {
	pos := len(a.calls)
	a.byIndex[evt.ToolCallIndex] = pos
	a.calls = append(a.calls, provider.ToolCall{ID: evt.ToolCallID, Name: evt.ToolCallName})
	a.argBuilders = append(a.argBuilders, "")
}

func (a *toolCallAccumulator) delta(evt provider.StreamEvent) {
	if pos, ok := a.byIndex[evt.ToolCallIndex]; ok {
		a.argBuilders[pos] += evt.ToolCallArgs
	}
}

func (a *toolCallAccumulator) finalize() []provider.ToolCall {
	for i := range a.calls {
		if i < len(a.argBuilders) {
			a.calls[i].Arguments = json.RawMessage(a.argBuilders[i])
		}
	}
	return a.calls
}

// collectWithDeltas reads all events from a stream, forwarding each to onDelta,
// and assembles them into a ChatResponse.
func collectWithDeltas(ch <-chan provider.StreamEvent, onDelta DeltaCallback) (*provider.ChatResponse, error) {
	var result provider.ChatResponse
	tca := newToolCallAccumulator()

	for evt := range ch {
		if onDelta != nil {
			onDelta(evt)
		}

		switch evt.Type {
		case provider.EventContentDelta:
			result.Content += evt.Content
		case provider.EventReasoningDelta:
			result.Reasoning += evt.Content
		case provider.EventToolCallBegin:
			tca.begin(evt)
		case provider.EventToolCallDelta:
			tca.delta(evt)
		case provider.EventUsage:
			if evt.InputTokens > result.InputTokens {
				result.InputTokens = evt.InputTokens
			}
			if evt.OutputTokens > result.OutputTokens {
				result.OutputTokens = evt.OutputTokens
			}
		case provider.EventError:
			return nil, evt.Err
		case provider.EventDone:
			// finalize
		}
	}

	if calls := tca.finalize(); len(calls) > 0 {
		result.ToolCalls = calls
	}
	return &result, nil
}

// executeToolCalls executes a list of tool calls and adds results to history.
// Returns the list of tool result messages that were added.
func executeToolCalls(ctx context.Context, proxy *mcp.Proxy, toolCalls []provider.ToolCall, onMessage MessageCallback) []provider.Message {
	toolResults := make([]provider.Message, 0, len(toolCalls))

	for _, toolCall := range toolCalls {
		// Execute tool via MCP proxy
		result, err := proxy.CallTool(ctx, toolCall.Name, toolCall.Arguments)

		if err != nil {
			// Add error result to history
			toolMsg := provider.Message{
				Role:       "tool",
				Content:    fmt.Sprintf("Error: %v", err),
				ToolCallID: toolCall.ID,
				CreatedAt:  time.Now(),
			}
			if onMessage != nil {
				onMessage(toolMsg)
			}
			toolResults = append(toolResults, toolMsg)
			continue
		}

		// Check if result is an error
		if result.IsError {
			errText := extractTextFromContent(result.Content)

			// Add error result to history
			toolMsg := provider.Message{
				Role:       "tool",
				Content:    errText,
				ToolCallID: toolCall.ID,
				CreatedAt:  time.Now(),
			}
			if onMessage != nil {
				onMessage(toolMsg)
			}
			toolResults = append(toolResults, toolMsg)
			continue
		}

		// Success - extract result text
		resultText := extractTextFromContent(result.Content)

		// Add tool result to history
		toolMsg := provider.Message{
			Role:       "tool",
			Content:    resultText,
			ToolCallID: toolCall.ID,
			CreatedAt:  time.Now(),
		}
		if onMessage != nil {
			onMessage(toolMsg)
		}
		toolResults = append(toolResults, toolMsg)
	}

	return toolResults
}

// maxToolResultLen is the character threshold above which a tool result is
// compacted once the LLM has already processed it.
const maxToolResultLen = 200

// reminderInterval is the number of tool-calling rounds between synthetic
// goal reminders. After this many rounds the loop injects a system message
// reciting the user's original request so it stays in the model's recent
// attention window.
const reminderInterval = 5

// compactOldToolResults replaces verbose tool-result messages that the LLM
// has already responded to with a short summary. A tool result is considered
// "processed" when an assistant message appears after it in the history.
//
// This reduces tokens sent to the provider during multi-round tool-calling
// turns. It operates on the local history copy inside ProcessTurn — the
// TUI's m.history is unaffected.
func compactOldToolResults(history []provider.Message) {
	// Find the index of the last assistant message — everything before it
	// has been seen by the LLM.
	lastAssistant := -1
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "assistant" {
			lastAssistant = i
			break
		}
	}
	if lastAssistant < 0 {
		return
	}

	for i := 0; i < lastAssistant; i++ {
		if history[i].Role != "tool" {
			continue
		}
		if len(history[i].Content) <= maxToolResultLen {
			continue
		}
		history[i].Content = trimToolResult(history[i].Content)
	}
}

// trimToolResult keeps the first line (usually a header like "Read file.go (200 lines):")
// plus a truncation notice. This preserves enough context for the LLM to know what
// tool was called and what it returned, without the full payload.
func trimToolResult(content string) string {
	firstNL := strings.Index(content, "\n")
	if firstNL < 0 || firstNL > maxToolResultLen {
		// No newline or very long first line — hard truncate.
		r := []rune(content)
		if len(r) > maxToolResultLen {
			return string(r[:maxToolResultLen]) + "\n[Truncated — already processed by assistant]"
		}
		return content
	}
	return content[:firstNL] + "\n[Truncated — already processed by assistant]"
}

// recitationPrefix marks synthetic recitation messages so stale ones can be
// removed before injecting a fresh one.
const recitationPrefix = "[Recitation]"

// injectRecitation manages the tail-of-context recitation that keeps the
// model focused during long tool-calling loops. It removes any stale
// recitation message and, when appropriate, appends a fresh one.
//
// Priority: if the agent has written a scratchpad (plan/notes), that is
// injected on every round. Otherwise, on every reminderInterval rounds,
// the user's original request is echoed as a fallback.
func injectRecitation(history []provider.Message, pad ScratchpadReader, round int) []provider.Message {
	// Strip any previous recitation. Use "user" role so recitation stays
	// in the conversation messages and does not get hoisted into the
	// Anthropic system blocks (which would invalidate the prompt cache).
	n := 0
	for _, m := range history {
		if m.Role == "user" && strings.HasPrefix(m.Content, recitationPrefix) {
			continue
		}
		history[n] = m
		n++
	}
	history = history[:n]

	// If the agent has a scratchpad, always inject it.
	if pad != nil {
		if plan := pad.Content(); plan != "" {
			return append(history, provider.Message{
				Role:      "user",
				Content:   recitationPrefix + " Current plan:\n" + plan,
				CreatedAt: time.Now(),
			})
		}
	}

	// Fallback: periodic goal reminder using the user's original request.
	// Search forward to find the first user message (the original request),
	// not the last one which may be a follow-up or synthetic message.
	if round > 0 && round%reminderInterval == 0 {
		var userContent string
		for _, m := range history {
			if m.Role == "user" {
				userContent = m.Content
				break
			}
		}
		if userContent != "" {
			return append(history, provider.Message{
				Role:      "user",
				Content:   recitationPrefix + " The user's request: " + userContent,
				CreatedAt: time.Now(),
			})
		}
	}

	return history
}

// extractTextFromContent extracts text from MCP content blocks.
func extractTextFromContent(content []mcp.ContentBlock) string {
	var text string
	for _, block := range content {
		if block.Type == "text" {
			text += block.Text
		}
	}
	return text
}
