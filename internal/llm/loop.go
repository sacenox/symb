// Package llm implements the LLM interaction loop with tool calling support.
package llm

import (
	"context"
	"fmt"
	"time"

	"github.com/xonecas/symb/internal/mcp"
	"github.com/xonecas/symb/internal/provider"
)

// MessageCallback is called when a message should be added to history and saved.
type MessageCallback func(msg provider.Message)

// ToolCallCallback is called when tool calls are about to be executed.
type ToolCallCallback func()

// ProcessTurnOptions holds configuration for processing a turn.
type ProcessTurnOptions struct {
	Provider      provider.Provider
	Proxy         *mcp.Proxy
	Tools         []mcp.Tool
	History       []provider.Message
	OnMessage     MessageCallback
	OnToolCall    ToolCallCallback // Optional: called before executing tool calls
	MaxToolRounds int
}

// ProcessTurn handles one conversation turn, which may involve tool calls.
// It returns an error if the LLM call fails or max rounds are exceeded.
func ProcessTurn(ctx context.Context, opts ProcessTurnOptions) error {
	if opts.MaxToolRounds == 0 {
		opts.MaxToolRounds = 20
	}

	for round := 0; round < opts.MaxToolRounds; round++ {
		// Convert MCP tools to provider format
		providerTools := make([]provider.Tool, len(opts.Tools))
		for i, t := range opts.Tools {
			providerTools[i] = provider.Tool{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			}
		}

		// Call LLM with history
		resp, err := opts.Provider.ChatWithTools(ctx, opts.History, providerTools)
		if err != nil {
			return fmt.Errorf("LLM call failed: %w", err)
		}

		// If no tool calls, we're done
		if len(resp.ToolCalls) == 0 {
			// Add assistant response to history
			assistantMsg := provider.Message{
				Role:      "assistant",
				Content:   resp.Content,
				Reasoning: resp.Reasoning,
				CreatedAt: time.Now(),
			}
			if opts.OnMessage != nil {
				opts.OnMessage(assistantMsg)
			}
			opts.History = append(opts.History, assistantMsg)

			return nil
		}

		// Tool calls present - add assistant message with tool calls to history
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
