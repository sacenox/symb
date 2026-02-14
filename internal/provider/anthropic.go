package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/rs/zerolog/log"
)

const roleSystem = "system"

// Anthropic Messages API request types.

type anthropicRequest struct {
	Model       string             `json:"model"`
	Messages    []anthropicMessage `json:"messages"`
	System      string             `json:"system,omitempty"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature float64            `json:"temperature,omitempty"`
	Stream      bool               `json:"stream"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
}

type anthropicMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string or []anthropicContentBlock
}

// anthropicTextBlock is a "text" content block.
type anthropicTextBlock struct {
	Type string `json:"type"` // "text"
	Text string `json:"text"`
}

// anthropicToolUseBlock is a "tool_use" content block.
type anthropicToolUseBlock struct {
	Type  string          `json:"type"` // "tool_use"
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// anthropicToolResultBlock is a "tool_result" content block.
type anthropicToolResultBlock struct {
	Type      string `json:"type"` // "tool_result"
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
}

type anthropicTool struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	InputSchema interface{} `json:"input_schema"`
}

// Anthropic SSE streaming response types.

type anthropicContentBlockStart struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type string `json:"type"` // "text" or "tool_use"
		Text string `json:"text,omitempty"`
		ID   string `json:"id,omitempty"`
		Name string `json:"name,omitempty"`
	} `json:"content_block"`
}

type anthropicContentBlockDelta struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta struct {
		Type        string `json:"type"` // "text_delta", "thinking_delta", "input_json_delta", "signature_delta"
		Text        string `json:"text,omitempty"`
		Thinking    string `json:"thinking,omitempty"`
		PartialJSON string `json:"partial_json,omitempty"`
	} `json:"delta"`
}

// toAnthropicMessages converts provider-agnostic messages to Anthropic Messages API format.
// Returns (system, messages) — system is extracted and hoisted out.
func toAnthropicMessages(messages []Message) (string, []anthropicMessage) {
	var systemParts []string
	var result []anthropicMessage

	for _, m := range messages {
		if m.Role == roleSystem {
			systemParts = append(systemParts, m.Content)
			continue
		}

		if m.Role == "tool" {
			// Tool results become user messages with tool_result content blocks
			result = append(result, anthropicMessage{
				Role: "user",
				Content: []anthropicToolResultBlock{
					{
						Type:      "tool_result",
						ToolUseID: m.ToolCallID,
						Content:   m.Content,
					},
				},
			})
			continue
		}

		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			// Assistant message with tool calls
			var blocks []interface{}
			if m.Content != "" {
				blocks = append(blocks, anthropicTextBlock{
					Type: "text",
					Text: m.Content,
				})
			}
			for _, tc := range m.ToolCalls {
				input := tc.Arguments
				if len(input) == 0 {
					input = json.RawMessage(`{}`)
				}
				blocks = append(blocks, anthropicToolUseBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: input,
				})
			}
			result = append(result, anthropicMessage{
				Role:    "assistant",
				Content: blocks,
			})
			continue
		}

		// Simple text message
		result = append(result, anthropicMessage{
			Role:    m.Role,
			Content: m.Content,
		})
	}

	return strings.Join(systemParts, "\n\n"), result
}

// toAnthropicTools converts provider-agnostic tools to Anthropic tool format.
func toAnthropicTools(tools []Tool) ([]anthropicTool, error) {
	if tools == nil {
		return nil, nil
	}
	result := make([]anthropicTool, len(tools))
	for i, t := range tools {
		var schema interface{}
		if len(t.Parameters) > 0 {
			if err := json.Unmarshal(t.Parameters, &schema); err != nil {
				return nil, err
			}
		}
		if schema == nil {
			schema = map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			}
		}
		result[i] = anthropicTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		}
	}
	return result, nil
}

// parseAnthropicSSEStream reads Anthropic Messages API SSE events and emits StreamEvents.
//
// Anthropic SSE format:
//
//	event: message_start / content_block_start / content_block_delta /
//	       content_block_stop / message_delta / message_stop / ping
//	data: { JSON payload }
func parseAnthropicSSEStream(ctx context.Context, reader io.Reader, ch chan<- StreamEvent) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 512*1024)

	// Track tool call index mapping: anthropic block index -> our tool call counter
	toolCallCount := 0
	blockIsToolUse := make(map[int]bool)
	blockToolIndex := make(map[int]int)

	var currentEventType string

	for scanner.Scan() {
		line := scanner.Text()

		// Parse event type line
		if strings.HasPrefix(line, "event: ") {
			currentEventType = strings.TrimPrefix(line, "event: ")
			continue
		}

		// Parse data line
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		switch currentEventType {
		case "message_stop":
			trySend(ctx, ch, StreamEvent{Type: EventDone})
			return

		case "content_block_start":
			var evt anthropicContentBlockStart
			if err := json.Unmarshal([]byte(data), &evt); err != nil {
				log.Warn().Err(err).Msg("Failed to parse anthropic content_block_start")
				continue
			}
			if evt.ContentBlock.Type == "tool_use" {
				idx := toolCallCount
				toolCallCount++
				blockIsToolUse[evt.Index] = true
				blockToolIndex[evt.Index] = idx
				if !trySend(ctx, ch, StreamEvent{
					Type:          EventToolCallBegin,
					ToolCallIndex: idx,
					ToolCallID:    evt.ContentBlock.ID,
					ToolCallName:  evt.ContentBlock.Name,
				}) {
					return
				}
			}

		case "content_block_delta":
			var evt anthropicContentBlockDelta
			if err := json.Unmarshal([]byte(data), &evt); err != nil {
				log.Warn().Err(err).Msg("Failed to parse anthropic content_block_delta")
				continue
			}

			switch evt.Delta.Type {
			case "text_delta":
				if evt.Delta.Text != "" {
					if !trySend(ctx, ch, StreamEvent{Type: EventContentDelta, Content: evt.Delta.Text}) {
						return
					}
				}
			case "thinking_delta":
				if evt.Delta.Thinking != "" {
					if !trySend(ctx, ch, StreamEvent{Type: EventReasoningDelta, Content: evt.Delta.Thinking}) {
						return
					}
				}
			case "input_json_delta":
				if evt.Delta.PartialJSON != "" && blockIsToolUse[evt.Index] {
					if !trySend(ctx, ch, StreamEvent{
						Type:          EventToolCallDelta,
						ToolCallIndex: blockToolIndex[evt.Index],
						ToolCallArgs:  evt.Delta.PartialJSON,
					}) {
						return
					}
				}
			}

		case "ping", "message_start", "message_delta", "content_block_stop":
			// Ignored
		}

		currentEventType = ""
	}

	if err := scanner.Err(); err != nil {
		trySend(ctx, ch, StreamEvent{Type: EventError, Err: err})
		return
	}

	// Stream ended without message_stop
	trySend(ctx, ch, StreamEvent{Type: EventDone})
}
