// Package llm implements the LLM interaction loop with tool calling support.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
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

// ProcessTurnOptions holds configuration for processing a turn.
type ProcessTurnOptions struct {
	Provider      provider.Provider
	Proxy         *mcp.Proxy
	Tools         []mcp.Tool
	History       []provider.Message
	OnMessage     MessageCallback
	OnDelta       DeltaCallback    // Optional: called for each stream event
	OnToolCall    ToolCallCallback // Optional: called before executing tool calls
	MaxToolRounds int
}

// ProcessTurn handles one conversation turn, which may involve tool calls.
// It streams events via OnDelta and emits complete messages via OnMessage.
func ProcessTurn(ctx context.Context, opts ProcessTurnOptions) error {
	if opts.MaxToolRounds == 0 {
		opts.MaxToolRounds = 20
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
		// Start streaming from LLM
		stream, err := opts.Provider.ChatStream(ctx, opts.History, providerTools)
		if err != nil {
			return fmt.Errorf("LLM call failed: %w", err)
		}

		// Read stream, forwarding deltas and accumulating the full response
		resp, err := collectWithDeltas(stream, opts.OnDelta)
		if err != nil {
			return fmt.Errorf("LLM stream failed: %w", err)
		}

		// Build assistant message from accumulated response
		assistantMsg := provider.Message{
			Role:      "assistant",
			Content:   resp.Content,
			Reasoning: resp.Reasoning,
			ToolCalls: resp.ToolCalls,
			CreatedAt: time.Now(),
		}
		if opts.OnMessage != nil {
			opts.OnMessage(assistantMsg)
		}
		opts.History = append(opts.History, assistantMsg)

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

	return fmt.Errorf("too many tool call rounds (limit: %d)", opts.MaxToolRounds)
}

// collectWithDeltas reads all events from a stream, forwarding each to onDelta,
// and assembles them into a ChatResponse.
func collectWithDeltas(ch <-chan provider.StreamEvent, onDelta DeltaCallback) (*provider.ChatResponse, error) {
	var result provider.ChatResponse
	// Map from tool call index to position in our slice
	toolCallsByIndex := make(map[int]int)
	var toolCalls []provider.ToolCall
	var toolArgBuilders []string

	for evt := range ch {
		// Forward delta to callback
		if onDelta != nil {
			onDelta(evt)
		}

		switch evt.Type {
		case provider.EventContentDelta:
			result.Content += evt.Content
		case provider.EventReasoningDelta:
			result.Reasoning += evt.Content
		case provider.EventToolCallBegin:
			pos := len(toolCalls)
			toolCallsByIndex[evt.ToolCallIndex] = pos
			toolCalls = append(toolCalls, provider.ToolCall{
				ID:   evt.ToolCallID,
				Name: evt.ToolCallName,
			})
			toolArgBuilders = append(toolArgBuilders, "")
		case provider.EventToolCallDelta:
			if pos, ok := toolCallsByIndex[evt.ToolCallIndex]; ok {
				toolArgBuilders[pos] += evt.ToolCallArgs
			}
		case provider.EventError:
			return nil, evt.Err
		case provider.EventDone:
			// finalize
		}
	}

	// Finalize tool call arguments
	for i := range toolCalls {
		if i < len(toolArgBuilders) {
			toolCalls[i].Arguments = json.RawMessage(toolArgBuilders[i])
		}
	}
	if len(toolCalls) > 0 {
		result.ToolCalls = toolCalls
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
