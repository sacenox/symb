package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/rs/zerolog/log"
	zen "github.com/sacenox/go-opencode-ai-zen-sdk"
)

type ZenProvider struct {
	name        string
	client      *zen.Client
	model       string
	temperature float64
}

func NewZen(name, apiKey, baseURL, model string, temperature float64) (*ZenProvider, error) {
	cfg := zen.Config{
		APIKey:  apiKey,
		BaseURL: baseURL,
	}
	client, err := zen.NewClient(cfg)
	if err != nil {
		return nil, err
	}

	return &ZenProvider{
		name:        name,
		client:      client,
		model:       model,
		temperature: temperature,
	}, nil
}

func (p *ZenProvider) Name() string {
	return p.name
}

func (p *ZenProvider) ChatStream(ctx context.Context, messages []Message, tools []Tool) (<-chan StreamEvent, error) {
	system, rest := splitSystem(messages)
	req := zen.NormalizedRequest{
		Model:    p.model,
		System:   system,
		Messages: toZenMessages(rest),
		Tools:    toZenTools(tools),
		Stream:   true,
	}

	if p.temperature > 0 {
		req.Temperature = &p.temperature
	}

	maxTokens := 16000
	req.MaxTokens = &maxTokens

	events, errs, err := p.client.UnifiedStreamNormalized(ctx, req)
	if err != nil {
		return nil, err
	}

	ch := make(chan StreamEvent)
	go func() {
		defer close(ch)
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					return
				}
				if !p.emitEvent(ctx, ch, ev) {
					return
				}
			case err, ok := <-errs:
				if ok && err != nil {
					var apiErr *zen.APIError
					if errors.As(err, &apiErr) {
						log.Error().
							Int("status", apiErr.StatusCode).
							Str("body", string(apiErr.Body)).
							Msg("zen: stream API error")
					}
					trySend(ctx, ch, StreamEvent{Type: EventError, Err: err})
				}
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch, nil
}

func (p *ZenProvider) emitEvent(ctx context.Context, ch chan<- StreamEvent, ev zen.UnifiedEvent) bool {
	data := ev.Data
	if len(data) == 0 || string(data) == "[DONE]" {
		return trySend(ctx, ch, StreamEvent{Type: EventDone})
	}

	switch ev.Endpoint {
	case zen.EndpointMessages:
		return p.emitAnthropicEvent(ctx, ch, ev.Event, data)
	case zen.EndpointModels:
		return p.emitGeminiEvent(ctx, ch, data)
	case zen.EndpointResponses:
		return p.emitResponsesEvent(ctx, ch, ev.Event, data)
	default:
		return p.emitChatCompletionsEvent(ctx, ch, data)
	}
}

// emitChatCompletionsEvent handles OpenAI chat completions SSE chunks.
func (p *ZenProvider) emitChatCompletionsEvent(ctx context.Context, ch chan<- StreamEvent, data json.RawMessage) bool {
	var chunk map[string]any
	if err := json.Unmarshal(data, &chunk); err != nil {
		return true
	}

	if usage, ok := chunk["usage"].(map[string]any); ok {
		if !trySend(ctx, ch, StreamEvent{
			Type:         EventUsage,
			InputTokens:  getIntOrZero(usage, "prompt_tokens"),
			OutputTokens: getIntOrZero(usage, "completion_tokens"),
		}) {
			return false
		}
	}

	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		delta, _ := chunk["delta"].(map[string]any)
		if delta != nil {
			return p.emitDelta(ctx, ch, delta)
		}
		return true
	}

	choice, _ := choices[0].(map[string]any)
	delta, _ := choice["delta"].(map[string]any)
	if delta == nil {
		return true
	}

	return p.emitDelta(ctx, ch, delta)
}

// emitAnthropicEvent handles Anthropic Messages SSE chunks.
// Relevant event types:
//   - content_block_start: carries tool_use id/name
//   - content_block_delta: text_delta or input_json_delta
//   - message_delta: usage
func (p *ZenProvider) emitAnthropicEvent(ctx context.Context, ch chan<- StreamEvent, event string, data json.RawMessage) bool {
	var chunk map[string]any
	if err := json.Unmarshal(data, &chunk); err != nil {
		return true
	}

	switch event {
	case "content_block_start":
		cb, _ := chunk["content_block"].(map[string]any)
		if getStringOrEmpty(cb, "type") == "tool_use" {
			idx := getIntOrZero(chunk, "index")
			if !trySend(ctx, ch, StreamEvent{
				Type:          EventToolCallBegin,
				ToolCallIndex: idx,
				ToolCallID:    getStringOrEmpty(cb, "id"),
				ToolCallName:  getStringOrEmpty(cb, "name"),
			}) {
				return false
			}
		}

	case "content_block_delta":
		idx := getIntOrZero(chunk, "index")
		delta, _ := chunk["delta"].(map[string]any)
		switch getStringOrEmpty(delta, "type") {
		case "text_delta":
			if text := getStringOrEmpty(delta, "text"); text != "" {
				if !trySend(ctx, ch, StreamEvent{Type: EventContentDelta, Content: text}) {
					return false
				}
			}
		case "thinking_delta":
			if thinking := getStringOrEmpty(delta, "thinking"); thinking != "" {
				if !trySend(ctx, ch, StreamEvent{Type: EventReasoningDelta, Content: thinking}) {
					return false
				}
			}
		case "input_json_delta":
			if args := getStringOrEmpty(delta, "partial_json"); args != "" {
				if !trySend(ctx, ch, StreamEvent{
					Type:          EventToolCallDelta,
					ToolCallIndex: idx,
					ToolCallArgs:  args,
				}) {
					return false
				}
			}
		}

	case "message_delta":
		if usage, ok := chunk["usage"].(map[string]any); ok {
			in := getIntOrZero(usage, "input_tokens")
			out := getIntOrZero(usage, "output_tokens")
			if in > 0 || out > 0 {
				if !trySend(ctx, ch, StreamEvent{
					Type:         EventUsage,
					InputTokens:  in,
					OutputTokens: out,
				}) {
					return false
				}
			}
		}
	}

	return true
}

// emitGeminiEvent handles Gemini SSE chunks.
// Each chunk has candidates[0].content.parts[].{text,functionCall}.
func (p *ZenProvider) emitGeminiEvent(ctx context.Context, ch chan<- StreamEvent, data json.RawMessage) bool {
	var chunk map[string]any
	if err := json.Unmarshal(data, &chunk); err != nil {
		return true
	}

	candidates, _ := chunk["candidates"].([]any)
	if len(candidates) == 0 {
		return true
	}
	candidate, _ := candidates[0].(map[string]any)
	content, _ := candidate["content"].(map[string]any)
	parts, _ := content["parts"].([]any)

	for idx, p2 := range parts {
		part, _ := p2.(map[string]any)
		if text := getStringOrEmpty(part, "text"); text != "" {
			if !trySend(ctx, ch, StreamEvent{Type: EventContentDelta, Content: text}) {
				return false
			}
		}
		if fc, ok := part["functionCall"].(map[string]any); ok {
			name := getStringOrEmpty(fc, "name")
			if name != "" {
				if !trySend(ctx, ch, StreamEvent{
					Type:          EventToolCallBegin,
					ToolCallIndex: idx,
					ToolCallName:  name,
				}) {
					return false
				}
			}
			if args, ok := fc["args"]; ok {
				argsJSON, err := json.Marshal(args)
				if err == nil {
					if !trySend(ctx, ch, StreamEvent{
						Type:          EventToolCallDelta,
						ToolCallIndex: idx,
						ToolCallArgs:  string(argsJSON),
					}) {
						return false
					}
				}
			}
		}
	}

	if meta, ok := chunk["usageMetadata"].(map[string]any); ok {
		in := getIntOrZero(meta, "promptTokenCount")
		out := getIntOrZero(meta, "candidatesTokenCount")
		if in > 0 || out > 0 {
			if !trySend(ctx, ch, StreamEvent{
				Type:         EventUsage,
				InputTokens:  in,
				OutputTokens: out,
			}) {
				return false
			}
		}
	}

	return true
}

// emitResponsesEvent handles OpenAI Responses API SSE chunks.
// Relevant event types: response.output_text.delta, response.function_call_arguments.delta,
// response.output_item.added (carries tool call id/name), response.completed (usage).
func (p *ZenProvider) emitResponsesEvent(ctx context.Context, ch chan<- StreamEvent, event string, data json.RawMessage) bool {
	var chunk map[string]any
	if err := json.Unmarshal(data, &chunk); err != nil {
		return true
	}

	switch event {
	case "response.output_text.delta":
		if delta := getStringOrEmpty(chunk, "delta"); delta != "" {
			if !trySend(ctx, ch, StreamEvent{Type: EventContentDelta, Content: delta}) {
				return false
			}
		}

	case "response.output_item.added":
		item, _ := chunk["item"].(map[string]any)
		if getStringOrEmpty(item, "type") == "function_call" {
			idx := getIntOrZero(chunk, "output_index")
			if !trySend(ctx, ch, StreamEvent{
				Type:          EventToolCallBegin,
				ToolCallIndex: idx,
				ToolCallID:    getStringOrEmpty(item, "call_id"),
				ToolCallName:  getStringOrEmpty(item, "name"),
			}) {
				return false
			}
		}

	case "response.function_call_arguments.delta":
		idx := getIntOrZero(chunk, "output_index")
		if delta := getStringOrEmpty(chunk, "delta"); delta != "" {
			if !trySend(ctx, ch, StreamEvent{
				Type:          EventToolCallDelta,
				ToolCallIndex: idx,
				ToolCallArgs:  delta,
			}) {
				return false
			}
		}

	case "response.completed":
		resp, _ := chunk["response"].(map[string]any)
		if usage, ok := resp["usage"].(map[string]any); ok {
			if !trySend(ctx, ch, StreamEvent{
				Type:         EventUsage,
				InputTokens:  getIntOrZero(usage, "input_tokens"),
				OutputTokens: getIntOrZero(usage, "output_tokens"),
			}) {
				return false
			}
		}
	}

	return true
}

func (p *ZenProvider) emitDelta(ctx context.Context, ch chan<- StreamEvent, delta map[string]any) bool {
	if reasoning := getStringOrEmpty(delta, "reasoning"); reasoning != "" {
		if !trySend(ctx, ch, StreamEvent{Type: EventReasoningDelta, Content: reasoning}) {
			return false
		}
	}
	if reasoning := getStringOrEmpty(delta, "reasoning_content"); reasoning != "" {
		if !trySend(ctx, ch, StreamEvent{Type: EventReasoningDelta, Content: reasoning}) {
			return false
		}
	}
	if content := getStringOrEmpty(delta, "content"); content != "" {
		if !trySend(ctx, ch, StreamEvent{Type: EventContentDelta, Content: content}) {
			return false
		}
	}

	toolCalls, _ := delta["tool_calls"].([]any)
	for _, tc := range toolCalls {
		toolCall, _ := tc.(map[string]any)
		idx := getIntOrZero(toolCall, "index")
		id := getStringOrEmpty(toolCall, "id")
		fn, _ := toolCall["function"].(map[string]any)
		name := getStringOrEmpty(fn, "name")
		args := getStringOrEmpty(fn, "arguments")

		if name != "" {
			if !trySend(ctx, ch, StreamEvent{
				Type:          EventToolCallBegin,
				ToolCallIndex: idx,
				ToolCallID:    id,
				ToolCallName:  name,
			}) {
				return false
			}
		}
		if args != "" {
			if !trySend(ctx, ch, StreamEvent{
				Type:          EventToolCallDelta,
				ToolCallIndex: idx,
				ToolCallArgs:  args,
			}) {
				return false
			}
		}
	}

	return true
}

func (p *ZenProvider) ListModels(ctx context.Context) ([]Model, error) {
	resp, err := p.client.ListModels(ctx)
	if err != nil {
		log.Error().Err(err).Str("provider", p.name).Msg("ListModels failed")
		return nil, err
	}

	models := make([]Model, len(resp.Data))
	for i, m := range resp.Data {
		models[i] = Model{
			Name: m.ID,
		}
	}
	log.Debug().Int("count", len(models)).Msg("ListModels success")
	return models, nil
}

func (p *ZenProvider) Close() error {
	return nil
}

func splitSystem(messages []Message) (system string, rest []Message) {
	var parts []string
	for _, m := range messages {
		if strings.EqualFold(m.Role, "system") || strings.EqualFold(m.Role, "developer") {
			if s := strings.TrimSpace(m.Content); s != "" {
				parts = append(parts, s)
			}
		} else {
			rest = append(rest, m)
		}
	}
	return strings.Join(parts, "\n\n"), rest
}

func toZenMessages(messages []Message) []zen.NormalizedMessage {
	result := make([]zen.NormalizedMessage, len(messages))
	for i, m := range messages {
		nm := zen.NormalizedMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		if len(m.ToolCalls) > 0 {
			nm.ToolCalls = make([]zen.NormalizedToolCall, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				nm.ToolCalls[j] = zen.NormalizedToolCall{
					ID:        tc.ID,
					Name:      tc.Name,
					Arguments: tc.Arguments,
				}
			}
		}
		result[i] = nm
	}
	return result
}

func toZenTools(tools []Tool) []zen.NormalizedTool {
	if len(tools) == 0 {
		return nil
	}
	result := make([]zen.NormalizedTool, len(tools))
	for i, t := range tools {
		params := t.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		result[i] = zen.NormalizedTool{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  params,
		}
	}
	return result
}

func getStringOrEmpty(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getIntOrZero(m map[string]any, key string) int {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		case json.Number:
			if i, err := n.Int64(); err == nil {
				return int(i)
			}
		}
	}
	return 0
}

type ZenFactory struct {
	name    string
	apiKey  string
	baseURL string
}

func NewZenFactory(name, apiKey, baseURL string) *ZenFactory {
	return &ZenFactory{
		name:    name,
		apiKey:  apiKey,
		baseURL: baseURL,
	}
}

func (f *ZenFactory) Name() string { return f.name }

func (f *ZenFactory) Create(model string, opts Options) Provider {
	baseURL := f.baseURL
	if baseURL == "" {
		baseURL = "https://opencode.ai/zen/v1"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	log.Info().
		Str("factory", f.name).
		Str("model", model).
		Bool("has_api_key", f.apiKey != "").
		Int("api_key_len", len(f.apiKey)).
		Str("base_url", baseURL).
		Msg("ZenFactory.Create")

	p, err := NewZen(f.name, f.apiKey, baseURL, model, opts.Temperature)
	if err != nil {
		panic("zen: failed to create provider: " + err.Error())
	}
	return p
}
