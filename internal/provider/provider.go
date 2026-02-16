// Package provider defines the LLM provider interface and implementations.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrProviderNotFound is returned when a requested provider doesn't exist.
var ErrProviderNotFound = errors.New("provider not found")

// Message represents a chat message.
type Message struct {
	Role         string
	Content      string
	Reasoning    string     // Model reasoning/thinking content (optional)
	ToolCalls    []ToolCall // For assistant messages with tool calls
	ToolCallID   string     // For tool result messages
	CreatedAt    time.Time  // Message timestamp
	InputTokens  int        // Token usage for this LLM call (assistant messages only)
	OutputTokens int        // Token usage for this LLM call (assistant messages only)
}

// Tool represents a tool/function definition for the LLM.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // JSON Schema
}

// ToolCall represents a tool call made by the LLM.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ChatResponse represents the response from a chat completion.
type ChatResponse struct {
	Content      string     // Text content (may be empty if tool calls)
	ToolCalls    []ToolCall // Tool calls (may be empty if text response)
	Reasoning    string     // Model reasoning content (optional)
	InputTokens  int        // Input/prompt token count (0 if unavailable)
	OutputTokens int        // Output/completion token count (0 if unavailable)
}

// StreamEventType identifies the kind of streaming event.
type StreamEventType int

const (
	// EventContentDelta carries a chunk of text content.
	EventContentDelta StreamEventType = iota
	// EventReasoningDelta carries a chunk of reasoning/thinking content.
	EventReasoningDelta
	// EventToolCallBegin signals the start of a new tool call with ID and name.
	EventToolCallBegin
	// EventToolCallDelta carries a chunk of tool call arguments.
	EventToolCallDelta
	// EventUsage carries token usage statistics.
	EventUsage
	// EventDone signals the stream is complete.
	EventDone
	// EventError signals a stream error.
	EventError
)

// StreamEvent represents a single event in a streamed LLM response.
type StreamEvent struct {
	Type StreamEventType

	// Content or reasoning text delta (for EventContentDelta, EventReasoningDelta).
	Content string

	// Tool call fields (for EventToolCallBegin, EventToolCallDelta).
	ToolCallIndex int    // Index of the tool call in the response (from OpenAI spec)
	ToolCallID    string // Set on EventToolCallBegin
	ToolCallName  string // Set on EventToolCallBegin
	ToolCallArgs  string // Argument fragment on EventToolCallDelta

	// Token usage (for EventUsage).
	InputTokens  int
	OutputTokens int

	// Error (for EventError).
	Err error
}

// Provider defines the interface for LLM providers.
type Provider interface {
	// Name returns the provider's identifier.
	Name() string

	// ChatStream sends messages with optional tools and returns a channel of streaming events.
	// The channel is closed after EventDone or EventError is sent.
	// Pass nil tools for simple chat without tool calling.
	ChatStream(ctx context.Context, messages []Message, tools []Tool) (<-chan StreamEvent, error)

	// Close closes idle HTTP connections and cleans up resources.
	Close() error
}

type Factory interface {
	Name() string
	Create(model string, opts Options) Provider
}

// Registry holds available providers.
type Registry struct {
	factories map[string]Factory
}

// NewRegistry creates a new provider registry.
func NewRegistry() *Registry {
	return &Registry{
		factories: make(map[string]Factory),
	}
}

func (r *Registry) RegisterFactory(name string, f Factory) {
	r.factories[name] = f
}

func (r *Registry) Create(name, model string, opts Options) (Provider, error) {
	f, ok := r.factories[name]
	if !ok {
		return nil, ErrProviderNotFound
	}
	return f.Create(model, opts), nil
}

// Options holds provider generation settings.
type Options struct {
	Temperature   float64
	TopP          float64
	RepeatPenalty float64
	MaxTokens     int
}

// List returns all registered provider names.
func (r *Registry) List() []string {
	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	return names
}
