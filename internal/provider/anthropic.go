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
	Model       string                `json:"model"`
	Messages    []anthropicMessage    `json:"messages"`
	System      []anthropicCacheBlock `json:"system,omitempty"`
	MaxTokens   int                   `json:"max_tokens"`
	Temperature float64               `json:"temperature,omitempty"`
	Stream      bool                  `json:"stream"`
	Tools       []anthropicTool       `json:"tools,omitempty"`
}

// anthropicCacheControl marks a block for prompt caching.
type anthropicCacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

// anthropicCacheBlock is a system prompt content block with optional cache_control.
type anthropicCacheBlock struct {
	Type         string                 `json:"type"` // "text"
	Text         string                 `json:"text"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
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
	Name         string                 `json:"name"`
	Description  string                 `json:"description,omitempty"`
	InputSchema  json.RawMessage        `json:"input_schema"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

// Anthropic SSE streaming response types.

// anthropicMessageStart wraps the message_start event payload.
type anthropicMessageStart struct {
	Message struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// anthropicMessageDelta wraps the message_delta event payload.
type anthropicMessageDelta struct {
	Usage struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

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
// Returns (system blocks, messages) — system is extracted and hoisted out.
// The last system block gets cache_control for prompt caching.
func toAnthropicMessages(messages []Message) ([]anthropicCacheBlock, []anthropicMessage) {
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

	var system []anthropicCacheBlock
	if len(systemParts) > 0 {
		system = make([]anthropicCacheBlock, len(systemParts))
		for i, part := range systemParts {
			system[i] = anthropicCacheBlock{Type: "text", Text: part}
		}
		// Mark last system block for prompt caching.
		system[len(system)-1].CacheControl = &anthropicCacheControl{Type: "ephemeral"}
	}
	return system, result
}

// toAnthropicTools converts provider-agnostic tools to Anthropic tool format.
// InputSchema is passed through as json.RawMessage to preserve deterministic
// serialization order (important for KV-cache hit rate).
func toAnthropicTools(tools []Tool) []anthropicTool {
	if tools == nil {
		return nil
	}
	emptySchema := json.RawMessage(`{"type":"object","properties":{}}`)
	result := make([]anthropicTool, len(tools))
	for i, t := range tools {
		schema := t.Parameters
		if len(schema) == 0 {
			schema = emptySchema
		}
		result[i] = anthropicTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		}
	}
	// Mark last tool for prompt caching. Anthropic caches the prefix up to
	// and including blocks with cache_control, so tools + system form a
	// stable cached prefix across turns.
	if len(result) > 0 {
		result[len(result)-1].CacheControl = &anthropicCacheControl{Type: "ephemeral"}
	}
	return result
}

// parseAnthropicSSEStream reads Anthropic Messages API SSE events and emits StreamEvents.
//
// Anthropic SSE format:
//
//	event: message_start / content_block_start / content_block_delta /
//	       content_block_stop / message_delta / message_stop / ping
//	data: { JSON payload }
//
// anthropicBlockTracker maps Anthropic block indices to tool call indices.
type anthropicBlockTracker struct {
	toolCallCount  int
	blockIsToolUse map[int]bool
	blockToolIndex map[int]int
}

func newAnthropicBlockTracker() *anthropicBlockTracker {
	return &anthropicBlockTracker{
		blockIsToolUse: make(map[int]bool),
		blockToolIndex: make(map[int]int),
	}
}

func parseAnthropicSSEStream(ctx context.Context, reader io.Reader, ch chan<- StreamEvent) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 512*1024)

	bt := newAnthropicBlockTracker()
	var currentEventType string

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "event: ") {
			currentEventType = strings.TrimPrefix(line, "event: ")
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		switch currentEventType {
		case "message_stop":
			trySend(ctx, ch, StreamEvent{Type: EventDone})
			return
		case "content_block_start":
			if !bt.handleBlockStart(ctx, ch, data) {
				return
			}
		case "content_block_delta":
			if !bt.handleBlockDelta(ctx, ch, data) {
				return
			}
		case "message_start":
			handleAnthropicMessageStart(ctx, ch, data)
		case "message_delta":
			handleAnthropicMessageDelta(ctx, ch, data)
		case "ping", "content_block_stop":
			// Ignored
		}

		currentEventType = ""
	}

	if err := scanner.Err(); err != nil {
		trySend(ctx, ch, StreamEvent{Type: EventError, Err: err})
		return
	}
	trySend(ctx, ch, StreamEvent{Type: EventDone})
}

// handleBlockStart processes a content_block_start event. Returns false if ctx cancelled.
func (bt *anthropicBlockTracker) handleBlockStart(ctx context.Context, ch chan<- StreamEvent, data string) bool {
	var evt anthropicContentBlockStart
	if err := json.Unmarshal([]byte(data), &evt); err != nil {
		log.Warn().Err(err).Msg("Failed to parse anthropic content_block_start")
		return true // continue scanning
	}
	if evt.ContentBlock.Type != "tool_use" {
		return true
	}
	idx := bt.toolCallCount
	bt.toolCallCount++
	bt.blockIsToolUse[evt.Index] = true
	bt.blockToolIndex[evt.Index] = idx
	return trySend(ctx, ch, StreamEvent{
		Type:          EventToolCallBegin,
		ToolCallIndex: idx,
		ToolCallID:    evt.ContentBlock.ID,
		ToolCallName:  evt.ContentBlock.Name,
	})
}

// handleBlockDelta processes a content_block_delta event. Returns false if ctx cancelled.
func (bt *anthropicBlockTracker) handleBlockDelta(ctx context.Context, ch chan<- StreamEvent, data string) bool {
	var evt anthropicContentBlockDelta
	if err := json.Unmarshal([]byte(data), &evt); err != nil {
		log.Warn().Err(err).Msg("Failed to parse anthropic content_block_delta")
		return true
	}
	switch evt.Delta.Type {
	case "text_delta":
		if evt.Delta.Text != "" {
			return trySend(ctx, ch, StreamEvent{Type: EventContentDelta, Content: evt.Delta.Text})
		}
	case "thinking_delta":
		if evt.Delta.Thinking != "" {
			return trySend(ctx, ch, StreamEvent{Type: EventReasoningDelta, Content: evt.Delta.Thinking})
		}
	case "input_json_delta":
		if evt.Delta.PartialJSON != "" && bt.blockIsToolUse[evt.Index] {
			return trySend(ctx, ch, StreamEvent{
				Type:          EventToolCallDelta,
				ToolCallIndex: bt.blockToolIndex[evt.Index],
				ToolCallArgs:  evt.Delta.PartialJSON,
			})
		}
	}
	return true
}

// handleAnthropicMessageStart extracts input token usage from message_start events.
func handleAnthropicMessageStart(ctx context.Context, ch chan<- StreamEvent, data string) {
	var ms anthropicMessageStart
	if err := json.Unmarshal([]byte(data), &ms); err != nil {
		return
	}
	if ms.Message.Usage.InputTokens > 0 || ms.Message.Usage.OutputTokens > 0 {
		trySend(ctx, ch, StreamEvent{
			Type:         EventUsage,
			InputTokens:  ms.Message.Usage.InputTokens,
			OutputTokens: ms.Message.Usage.OutputTokens,
		})
	}
}

// handleAnthropicMessageDelta extracts output token usage from message_delta events.
func handleAnthropicMessageDelta(ctx context.Context, ch chan<- StreamEvent, data string) {
	var md anthropicMessageDelta
	if err := json.Unmarshal([]byte(data), &md); err != nil {
		return
	}
	if md.Usage.OutputTokens > 0 {
		trySend(ctx, ch, StreamEvent{
			Type:         EventUsage,
			OutputTokens: md.Usage.OutputTokens,
		})
	}
}
