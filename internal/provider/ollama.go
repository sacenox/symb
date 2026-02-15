package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"
)

// OllamaProvider implements the Provider interface for Ollama.
type OllamaProvider struct {
	name        string
	baseURL     string
	httpClient  *http.Client
	model       string
	temperature float64
}

// NewOllama creates a new Ollama provider.
// Ollama exposes an OpenAI-compatible API at /v1.
func NewOllama(endpoint, model string) *OllamaProvider {
	return NewOllamaWithTemp("ollama", endpoint, model, 0.7)
}

func NewOllamaWithTemp(name string, endpoint, model string, temperature float64) *OllamaProvider {
	baseURL := strings.TrimRight(endpoint, "/") + "/v1"

	return &OllamaProvider{
		name:        name,
		baseURL:     baseURL,
		httpClient:  &http.Client{},
		model:       model,
		temperature: temperature,
	}
}

// Name returns the provider identifier.
func (p *OllamaProvider) Name() string {
	return p.name
}

// ChatStream sends messages with optional tools and returns a channel of streaming events.
func (p *OllamaProvider) ChatStream(ctx context.Context, messages []Message, tools []Tool) (<-chan StreamEvent, error) {
	req := ollamaChatRequest{
		Model:         p.model,
		Messages:      mergeConsecutiveSystemMessagesOllama(toOllamaMessages(messages)),
		Tools:         toOllamaTools(tools),
		Temperature:   float32(p.temperature),
		Stream:        true,
		StreamOptions: &chatStreamOptions{IncludeUsage: true},
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	reader, err := httpDoSSE(ctx, httpRequestConfig{
		client:   p.httpClient,
		url:      p.baseURL + "/chat/completions",
		body:     body,
		provider: "ollama",
		model:    p.model,
	})
	if err != nil {
		return nil, err
	}

	ch := make(chan StreamEvent)
	go func() {
		defer close(ch)
		defer reader.Close()
		parseSSEStream(ctx, reader, ch)
	}()

	return ch, nil
}

// OLLAMA-SPECIFIC TYPES
// These types are custom to Ollama and should NOT be used by other providers.
// They differ from OpenAI standard in structure and field names.

type ollamaChatRequest struct {
	Model         string             `json:"model"`
	Messages      []ollamaReqMessage `json:"messages"`
	Tools         []ollamaReqTool    `json:"tools,omitempty"`
	Temperature   float32            `json:"temperature,omitempty"`
	Stream        bool               `json:"stream"`
	StreamOptions *chatStreamOptions `json:"stream_options,omitempty"`
}

type ollamaReqMessage struct {
	Role       string              `json:"role"`
	Content    string              `json:"content"`
	ToolCallID string              `json:"tool_call_id,omitempty"`
	ToolCalls  []ollamaReqToolCall `json:"tool_calls,omitempty"`
}

type ollamaReqTool struct {
	Type     string            `json:"type"`
	Function ollamaReqFunction `json:"function"`
}

type ollamaReqFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ollamaReqToolCall struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Function ollamaReqFuncCall `json:"function"`
}

type ollamaReqFuncCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// toOllamaMessages converts provider messages to Ollama's custom request format.
//
// OLLAMA-SPECIFIC: Uses custom ollamaReqMessage type instead of OpenAI SDK types.
// Ollama has its own message format and tool call structure.
func toOllamaMessages(messages []Message) []ollamaReqMessage {
	result := make([]ollamaReqMessage, len(messages))
	for i, m := range messages {
		msg := ollamaReqMessage{
			Role:    m.Role,
			Content: m.Content,
		}

		if m.ToolCallID != "" {
			msg.ToolCallID = m.ToolCallID
		}

		if len(m.ToolCalls) > 0 {
			msg.ToolCalls = make([]ollamaReqToolCall, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				msg.ToolCalls[j] = ollamaReqToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: ollamaReqFuncCall{
						Name:      tc.Name,
						Arguments: string(tc.Arguments),
					},
				}
			}
		}

		result[i] = msg
	}
	return result
}

// toOllamaTools converts provider-agnostic tools to Ollama tool format.
// Parameters is passed through as json.RawMessage to preserve deterministic
// serialization order (important for KV-cache hit rate).
func toOllamaTools(tools []Tool) []ollamaReqTool {
	emptyParams := json.RawMessage(`{"type":"object","properties":{}}`)
	result := make([]ollamaReqTool, len(tools))
	for i, t := range tools {
		params := t.Parameters
		if len(params) == 0 {
			params = emptyParams
		}

		result[i] = ollamaReqTool{
			Type: "function",
			Function: ollamaReqFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		}
	}
	return result
}

// mergeConsecutiveSystemMessagesOllama merges consecutive system messages into a single message.
//
// OLLAMA-SPECIFIC BEHAVIOR:
// Unlike OpenAI, Ollama allows system messages anywhere in the conversation.
// This function merges consecutive system messages IN PLACE without moving them to the start.
// This preserves Ollama's flexible system message handling.
//
// DO NOT use this function for OpenAI-compatible providers - use mergeSystemMessagesOpenAI instead.
func mergeConsecutiveSystemMessagesOllama(messages []ollamaReqMessage) []ollamaReqMessage {
	if len(messages) == 0 {
		return messages
	}

	result := make([]ollamaReqMessage, 0, len(messages))
	var systemBuffer strings.Builder
	inSystemRun := false

	for i, msg := range messages {
		if msg.Role == roleSystem {
			// Start or continue system message accumulation
			if inSystemRun {
				systemBuffer.WriteString("\n\n")
			} else {
				inSystemRun = true
			}
			systemBuffer.WriteString(msg.Content)
		} else {
			// Flush accumulated system messages if any
			if inSystemRun {
				result = append(result, ollamaReqMessage{
					Role:    roleSystem,
					Content: systemBuffer.String(),
				})
				systemBuffer.Reset()
				inSystemRun = false
			}
			// Add non-system message
			result = append(result, msg)
		}

		// Flush at end of array
		if i == len(messages)-1 && inSystemRun {
			result = append(result, ollamaReqMessage{
				Role:    roleSystem,
				Content: systemBuffer.String(),
			})
		}
	}

	log.Debug().
		Int("original_count", len(messages)).
		Int("merged_count", len(result)).
		Msg("Ollama: Merged consecutive system messages")

	return result
}

// Close closes idle HTTP connections
func (p *OllamaProvider) Close() error {
	if p.httpClient != nil {
		p.httpClient.CloseIdleConnections()
	}
	return nil
}
