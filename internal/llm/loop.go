// Package llm implements the LLM interaction loop with tool calling support.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/xonecas/symb/internal/mcp"
	"github.com/xonecas/symb/internal/provider"
)

const (
	// MaxDepth is the maximum recursion depth for sub-agents.
	// Matches subagent.MaxSubAgentDepth to prevent import cycle.
	MaxDepth = 1
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
	Depth         int // Recursion depth (0=root agent, 1=sub-agent)
}

// streamAndCollect runs one LLM call: streams events, collects the response,
// reports usage, and returns the ChatResponse.
func streamAndCollect(ctx context.Context, opts *ProcessTurnOptions, tools []provider.Tool) (*provider.ChatResponse, error) {
	const maxEmptyRetries = 1

	for attempt := 0; attempt <= maxEmptyRetries; attempt++ {
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
		if !isEmptyResponse(resp) {
			return resp, nil
		}

		log.Warn().
			Str("provider", opts.Provider.Name()).
			Int("attempt", attempt+1).
			Msg("Empty response from provider")
	}

	return nil, fmt.Errorf("empty response from provider %s", opts.Provider.Name())
}

func isEmptyResponse(resp *provider.ChatResponse) bool {
	if resp == nil {
		return true
	}
	return resp.Content == "" && resp.Reasoning == "" && len(resp.ToolCalls) == 0
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
type recentCall struct {
	Name string
	Args string
}

func ProcessTurn(ctx context.Context, opts ProcessTurnOptions) error {
	// Enforce max depth to prevent infinite recursion
	if opts.Depth > MaxDepth {
		return fmt.Errorf("max sub-agent depth exceeded: %d > %d", opts.Depth, MaxDepth)
	}

	if opts.MaxToolRounds == 0 {
		opts.MaxToolRounds = 60
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

	var recent []recentCall
	for round := 0; round < opts.MaxToolRounds; round++ {
		// Inject a <system-reminder> into the last tool result to keep
		// the model focused. Two sources:
		// 1. Scratchpad (agent-written plan) — preferred when present.
		// 2. Goal reminder (user's original request) — fallback.
		injectRecitation(opts.History, opts.Scratchpad, round)

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

		for _, tc := range resp.ToolCalls {
			recent = append(recent, recentCall{Name: tc.Name, Args: string(tc.Arguments)})
		}
		if len(recent) >= 3 {
			last3 := recent[len(recent)-3:]
			if last3[0] == last3[1] && last3[1] == last3[2] {
				if len(toolResults) > 0 {
					last := &toolResults[len(toolResults)-1]
					last.Content += "\n\n<system-reminder>WARNING: You are repeating the same tool call with the same arguments. This is wasteful. Stop and either try a different approach, summarize what you know, or ask the user for help.</system-reminder>"
					opts.History[len(opts.History)-1] = *last
				}
			}
		}

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

// reminderInterval is the number of tool-calling rounds between synthetic
// goal reminders. After this many rounds the loop injects a system message
// reciting the user's original request so it stays in the model's recent
// attention window.
const reminderInterval = 10

// injectRecitation appends a <system-reminder> block to the last tool-result
// message in history to keep the model focused during long tool-calling loops.
// By appending to an existing message instead of creating a new one, we avoid
// shifting message positions and invalidating the Anthropic prompt cache.
//
// Priority: if the agent has written a scratchpad (plan/notes), that is
// injected. Otherwise the user's original request is echoed as a fallback.
func injectRecitation(history []provider.Message, pad ScratchpadReader, round int) {
	if round == 0 || round%reminderInterval != 0 {
		return
	}

	// Build the reminder text.
	var reminder string
	if pad != nil {
		if plan := pad.Content(); plan != "" {
			reminder = plan
		}
	}
	if reminder == "" {
		// Fallback: echo the user's original request.
		for _, m := range history {
			if m.Role == "user" {
				reminder = "The user's request: " + m.Content
				break
			}
		}
	}
	if reminder == "" {
		return
	}

	// Append to the last tool-result message, stripping any prior
	// reminder on that same message to avoid token accumulation.
	tag := "\n\n<system-reminder>\n"
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "tool" {
			if idx := strings.Index(history[i].Content, tag); idx >= 0 {
				history[i].Content = history[i].Content[:idx]
			}
			history[i].Content += tag + reminder + "\n</system-reminder>"
			return
		}
	}
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
