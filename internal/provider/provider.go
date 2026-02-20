// Package provider defines the LLM provider interface and implementations.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/rs/zerolog/log"
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
	FunctionName string     // For tool result messages: name of the called function (required by Gemini)
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
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Arguments        json.RawMessage `json:"arguments"`
	ThoughtSignature string          `json:"thought_signature,omitempty"`
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
	ToolCallIndex     int    // Index of the tool call in the response (from OpenAI spec)
	ToolCallID        string // Set on EventToolCallBegin
	ToolCallName      string // Set on EventToolCallBegin
	ToolCallSignature string // Optional thought signature for Gemini tool calls
	ToolCallArgs      string // Argument fragment on EventToolCallDelta

	// Token usage (for EventUsage).
	InputTokens  int
	OutputTokens int

	// Error (for EventError).
	Err error
}

type Model struct {
	Name       string
	Size       int64
	Digest     string
	ModifiedAt time.Time
	Format     string
	Family     string
	ParamSize  string
	QuantLevel string
}

// Provider defines the interface for LLM providers.
type Provider interface {
	// Name returns the provider's identifier.
	Name() string

	// ChatStream sends messages with optional tools and returns a channel of streaming events.
	// The channel is closed after EventDone or EventError is sent.
	// Pass nil tools for simple chat without tool calling.
	ChatStream(ctx context.Context, messages []Message, tools []Tool) (<-chan StreamEvent, error)

	// ListModels returns available models from the provider.
	ListModels(ctx context.Context) ([]Model, error)

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
		log.Error().Str("name", name).Str("model", model).Msg("Registry.Create: factory not found")
		return nil, ErrProviderNotFound
	}
	log.Info().Str("name", name).Str("model", model).Str("factory_type", "unknown").Msg("Registry.Create: calling factory")
	return f.Create(model, opts), nil
}

// Options holds provider generation settings.
type Options struct {
	Temperature float64
}

// List returns all registered provider names.
func (r *Registry) List() []string {
	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	return names
}

// TaggedModel pairs a provider config name with a model.
type TaggedModel struct {
	ProviderName string
	Model        Model
}

// ListAllModels concurrently fetches models from every registered provider and
// returns the combined list. Errors from individual providers are logged and
// skipped so a single unavailable provider does not block the rest.
func (r *Registry) ListAllModels(ctx context.Context, opts Options) []TaggedModel {
	type result struct {
		name   string
		models []Model
	}
	ch := make(chan result, len(r.factories))
	for name := range r.factories {
		name := name
		go func() {
			prov := r.factories[name].Create("", opts)
			models, err := prov.ListModels(ctx)
			prov.Close()
			if err != nil {
				log.Warn().Str("provider", name).Err(err).Msg("ListAllModels: provider error")
				ch <- result{name: name}
				return
			}
			ch <- result{name: name, models: models}
		}()
	}
	var all []TaggedModel
	for range r.factories {
		res := <-ch
		for _, m := range res.models {
			all = append(all, TaggedModel{ProviderName: res.name, Model: m})
		}
	}
	return all
}
