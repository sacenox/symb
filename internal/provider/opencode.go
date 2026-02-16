package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

// openCodeRequest is a custom request struct to ensure stream:false is serialized
// The openai.ChatCompletionRequest has omitempty on Stream, which omits false values
type openCodeRequest struct {
	Model         string                         `json:"model"`
	Messages      []openai.ChatCompletionMessage `json:"messages"`
	Tools         []openai.Tool                  `json:"tools,omitempty"`
	Temperature   float32                        `json:"temperature,omitempty"`
	Stream        bool                           `json:"stream"` // NO omitempty - always serialize
	StreamOptions *chatStreamOptions             `json:"stream_options,omitempty"`
}

// OpenCodeProvider implements the Provider interface for OpenCode Zen.
type OpenCodeProvider struct {
	name        string
	baseURL     string
	apiKey      string
	httpClient  *http.Client
	model       string
	temperature float64
}

const (
	opencodeChatCompletionsEndpoint = "/chat/completions"
	opencodeMessagesEndpoint        = "/messages"
	opencodeResponsesEndpoint       = "/responses"
)

// opencodeModelEndpoints overrides the prefix-based fallback in opencodeEndpointForModel.
// Only list models whose endpoint differs from what the prefix logic would choose.
var opencodeModelEndpoints = map[string]string{}

// NewOpenCode creates a new OpenCode Zen provider.
func NewOpenCode(endpoint, model, apiKey string) *OpenCodeProvider {
	return NewOpenCodeWithTemp("opencode_zen", endpoint, model, apiKey, 0.7)
}

func NewOpenCodeWithTemp(name string, endpoint, model, apiKey string, temperature float64) *OpenCodeProvider {
	baseURL := strings.TrimRight(endpoint, "/")

	return &OpenCodeProvider{
		name:        name,
		baseURL:     baseURL,
		apiKey:      apiKey,
		httpClient:  &http.Client{},
		model:       model,
		temperature: temperature,
	}
}

// Name returns the provider identifier.
func (p *OpenCodeProvider) Name() string {
	return p.name
}

// ChatStream sends messages with optional tools and returns a channel of streaming events.
func (p *OpenCodeProvider) ChatStream(ctx context.Context, messages []Message, tools []Tool) (<-chan StreamEvent, error) {
	endpoint := opencodeEndpointForModel(p.model)

	switch endpoint {
	case opencodeMessagesEndpoint:
		return p.chatStreamAnthropic(ctx, messages, tools)
	case opencodeChatCompletionsEndpoint:
		return p.chatStreamOpenAI(ctx, messages, tools)
	case opencodeResponsesEndpoint:
		return p.chatStreamResponses(ctx, messages, tools)
	default:
		return nil, fmt.Errorf("opencode model %q uses unsupported endpoint %q", p.model, endpoint)
	}
}

// chatStreamOpenAI streams via the OpenAI-compatible /chat/completions endpoint.
func (p *OpenCodeProvider) chatStreamOpenAI(ctx context.Context, messages []Message, tools []Tool) (<-chan StreamEvent, error) {
	openaiTools := toOpenAITools(tools)

	customReq := openCodeRequest{
		Model:         p.model,
		Messages:      mergeSystemMessagesOpenAI(toOpenAIMessages(messages)),
		Tools:         openaiTools,
		Temperature:   float32(p.temperature),
		Stream:        true,
		StreamOptions: &chatStreamOptions{IncludeUsage: true},
	}
	body, err := json.Marshal(customReq)
	if err != nil {
		return nil, err
	}

	reader, err := httpDoSSE(ctx, httpRequestConfig{
		client:   p.httpClient,
		url:      p.baseURL + opencodeChatCompletionsEndpoint,
		body:     body,
		headers:  p.authHeaders(),
		provider: p.name,
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

// chatStreamAnthropic streams via the Anthropic /messages endpoint.
func (p *OpenCodeProvider) chatStreamAnthropic(ctx context.Context, messages []Message, tools []Tool) (<-chan StreamEvent, error) {
	system, anthropicMsgs := toAnthropicMessages(messages)

	anthropicTools := toAnthropicTools(tools)

	req := anthropicRequest{
		Model:       p.model,
		Messages:    anthropicMsgs,
		System:      system,
		MaxTokens:   16384,
		Temperature: p.temperature,
		Stream:      true,
		Tools:       anthropicTools,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	headers := p.authHeaders()
	headers["x-api-key"] = p.apiKey
	headers["anthropic-version"] = "2023-06-01"
	headers["anthropic-beta"] = "prompt-caching-2024-07-31"

	reader, err := httpDoSSE(ctx, httpRequestConfig{
		client:   p.httpClient,
		url:      p.baseURL + opencodeMessagesEndpoint,
		body:     body,
		headers:  headers,
		provider: p.name,
		model:    p.model,
	})
	if err != nil {
		return nil, err
	}

	ch := make(chan StreamEvent)
	go func() {
		defer close(ch)
		defer reader.Close()
		parseAnthropicSSEStream(ctx, reader, ch)
	}()

	return ch, nil
}

// chatStreamResponses streams via the OpenAI Responses API /responses endpoint.
func (p *OpenCodeProvider) chatStreamResponses(ctx context.Context, messages []Message, tools []Tool) (<-chan StreamEvent, error) {
	req := responsesRequest{
		Model:  p.model,
		Input:  toResponsesInput(messages),
		Tools:  toResponsesTools(tools),
		Stream: true,
	}
	if !strings.Contains(p.model, "codex") {
		t := float32(p.temperature)
		req.Temperature = &t
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	reader, err := httpDoSSE(ctx, httpRequestConfig{
		client:   p.httpClient,
		url:      p.baseURL + opencodeResponsesEndpoint,
		body:     body,
		headers:  p.authHeaders(),
		provider: p.name,
		model:    p.model,
	})
	if err != nil {
		return nil, err
	}

	ch := make(chan StreamEvent)
	go func() {
		defer close(ch)
		defer reader.Close()
		parseResponsesSSEStream(ctx, reader, ch)
	}()

	return ch, nil
}

// authHeaders returns common auth headers for requests.
func (p *OpenCodeProvider) authHeaders() map[string]string {
	headers := make(map[string]string)
	if p.apiKey != "" {
		headers["Authorization"] = "Bearer " + p.apiKey
	}
	return headers
}

func opencodeEndpointForModel(model string) string {
	if endpoint, ok := opencodeModelEndpoints[model]; ok {
		return endpoint
	}

	switch {
	case strings.HasPrefix(model, "gpt-"):
		return opencodeResponsesEndpoint
	case strings.HasPrefix(model, "claude-"):
		return opencodeMessagesEndpoint
	default:
		return opencodeChatCompletionsEndpoint
	}
}

// Close closes idle HTTP connections
func (p *OpenCodeProvider) Close() error {
	if p.httpClient != nil {
		p.httpClient.CloseIdleConnections()
	}
	return nil
}
