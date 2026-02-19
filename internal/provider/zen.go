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
		Model:      p.model,
		System:     system,
		Messages:   toZenMessages(rest),
		Tools:      toZenTools(tools),
		Reasoning:  &zen.NormalizedReasoning{Effort: "low"},
		ToolChoice: &zen.NormalizedToolChoice{Type: zen.ToolChoiceAuto},
	}

	if p.temperature > 0 {
		req.Temperature = &p.temperature
	}

	deltas, errs, err := p.client.Stream(ctx, req)
	if err != nil {
		var apiErr *zen.APIError
		if errors.As(err, &apiErr) {
			log.Error().
				Int("status", apiErr.StatusCode).
				Str("body", string(apiErr.Body)).
				Str("model", p.model).
				Msg("zen: Stream failed")
		}
		return nil, err
	}

	ch := make(chan StreamEvent)
	go func() {
		defer close(ch)
		for d := range deltas {
			var evt StreamEvent
			switch d.Type {
			case zen.DeltaText:
				evt = StreamEvent{Type: EventContentDelta, Content: d.Content}
			case zen.DeltaReasoning:
				evt = StreamEvent{Type: EventReasoningDelta, Content: d.Content}
			case zen.DeltaToolCallBegin:
				evt = StreamEvent{
					Type:          EventToolCallBegin,
					ToolCallIndex: d.ToolCallIndex,
					ToolCallID:    d.ToolCallID,
					ToolCallName:  d.ToolCallName,
				}
			case zen.DeltaToolCallArgumentsDelta:
				evt = StreamEvent{
					Type:          EventToolCallDelta,
					ToolCallIndex: d.ToolCallIndex,
					ToolCallArgs:  d.ArgumentsDelta,
				}
			case zen.DeltaToolCallDone:
				// The accumulator in the caller handles final args; skip.
				continue
			case zen.DeltaUsage:
				evt = StreamEvent{
					Type:         EventUsage,
					InputTokens:  d.InputTokens,
					OutputTokens: d.OutputTokens,
				}
			case zen.DeltaDone:
				evt = StreamEvent{Type: EventDone}
			default:
				continue
			}
			if !trySend(ctx, ch, evt) {
				// Context cancelled: drain the delta channel so SDK goroutines
				// can exit, then drain the error channel before returning.
				go func() {
					for range deltas {
					}
					<-errs
				}()
				return
			}
		}
		if err := <-errs; err != nil {
			var apiErr *zen.APIError
			if errors.As(err, &apiErr) {
				log.Error().
					Int("status", apiErr.StatusCode).
					Str("body", string(apiErr.Body)).
					Msg("zen: stream API error")
			}
			trySend(ctx, ch, StreamEvent{Type: EventError, Err: err})
		}
	}()

	return ch, nil
}

func (p *ZenProvider) ListModels(ctx context.Context) ([]Model, error) {
	resp, err := p.client.ListModels(ctx)
	if err != nil {
		log.Error().Err(err).Str("provider", p.name).Msg("ListModels failed")
		return nil, err
	}

	models := make([]Model, len(resp.Data))
	for i, m := range resp.Data {
		models[i] = Model{Name: m.ID}
	}
	log.Debug().Int("count", len(models)).Msg("ListModels success")
	return models, nil
}

func (p *ZenProvider) Close() error {
	return nil
}

// splitSystem separates system/developer messages from the rest.
// The Zen SDK takes system content as a top-level field on NormalizedRequest.
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
			Role:         m.Role,
			Content:      m.Content,
			ToolCallID:   m.ToolCallID,
			FunctionName: m.FunctionName,
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
			params = []byte(`{"type":"object","properties":{}}`)
		} else {
			params = stripSchemaKeys(params)
		}
		result[i] = zen.NormalizedTool{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  params,
		}
	}
	return result
}

// stripSchemaKeys removes JSON Schema meta-keys that Gemini rejects as unknown
// fields ($schema, $id, $defs, definitions, additionalProperties).
// It operates on the top-level object only, which is where MCP tools place them.
func stripSchemaKeys(raw []byte) []byte {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw // not a JSON object, return as-is
	}
	changed := false
	for _, key := range []string{"$schema", "$id", "$defs", "definitions", "additionalProperties"} {
		if _, ok := m[key]; ok {
			delete(m, key)
			changed = true
		}
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
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
		Str("base_url", baseURL).
		Msg("ZenFactory.Create")

	p, err := NewZen(f.name, f.apiKey, baseURL, model, opts.Temperature)
	if err != nil {
		panic("zen: failed to create provider: " + err.Error())
	}
	return p
}
