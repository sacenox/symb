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
	Model       string                         `json:"model"`
	Messages    []openai.ChatCompletionMessage `json:"messages"`
	Tools       []openai.Tool                  `json:"tools,omitempty"`
	Temperature float32                        `json:"temperature,omitempty"`
	Stream      bool                           `json:"stream"` // NO omitempty - always serialize
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

var opencodeModelEndpoints = map[string]string{
	"big-pickle":                 opencodeChatCompletionsEndpoint,
	"gemini-3-pro":               "/models/gemini-3-pro",
	"gemini-3-flash":             "/models/gemini-3-flash",
	"glm-4.7-free":               opencodeChatCompletionsEndpoint,
	"gpt-5-nano":                 opencodeChatCompletionsEndpoint, // Using chat/completions despite docs saying /responses (500 errors)
	"kimi-k2.5-free":             opencodeChatCompletionsEndpoint,
	"minimax-m2.1-free":          opencodeMessagesEndpoint,
	"trinity-large-preview-free": opencodeChatCompletionsEndpoint,
}

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
	if opencodeEndpointForModel(p.model) != opencodeChatCompletionsEndpoint {
		return nil, fmt.Errorf("opencode model %q does not support streaming via chat completions endpoint", p.model)
	}

	openaiTools, err := toOpenAITools(tools)
	if err != nil {
		return nil, fmt.Errorf("invalid tool schema: %w", err)
	}

	customReq := openCodeRequest{
		Model:       p.model,
		Messages:    mergeSystemMessagesOpenAI(toOpenAIMessages(messages)),
		Tools:       openaiTools,
		Temperature: float32(p.temperature),
		Stream:      true,
	}
	body, err := json.Marshal(customReq)
	if err != nil {
		return nil, err
	}

	headers := make(map[string]string)
	if p.apiKey != "" {
		headers["Authorization"] = "Bearer " + p.apiKey
	}

	reader, err := httpDoSSE(ctx, httpRequestConfig{
		client:   p.httpClient,
		url:      p.baseURL + opencodeEndpointForModel(p.model),
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
		parseSSEStream(ctx, reader, ch)
	}()

	return ch, nil
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
