package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

type vllmChatRequest struct {
	Model             string                         `json:"model"`
	Messages          []openai.ChatCompletionMessage `json:"messages"`
	Tools             []openai.Tool                  `json:"tools,omitempty"`
	Temperature       float32                        `json:"temperature,omitempty"`
	TopP              float32                        `json:"top_p,omitempty"`
	RepetitionPenalty float32                        `json:"repetition_penalty,omitempty"`
	MaxTokens         int                            `json:"max_tokens,omitempty"`
	Stream            bool                           `json:"stream"`
	StreamOptions     *chatStreamOptions             `json:"stream_options,omitempty"`
}

// VLLMProvider implements the Provider interface for vLLM.
type VLLMProvider struct {
	name          string
	baseURL       string
	apiKey        string
	httpClient    *http.Client
	model         string
	temperature   float64
	topP          float64
	repeatPenalty float64
	maxTokens     int
}

// NewVLLM creates a new vLLM provider.
func NewVLLM(endpoint, model, apiKey string) *VLLMProvider {
	return NewVLLMWithTemp("vllm", endpoint, model, apiKey, Options{Temperature: 0.7})
}

func NewVLLMWithTemp(name, endpoint, model, apiKey string, opts Options) *VLLMProvider {
	baseURL := strings.TrimRight(endpoint, "/")

	return &VLLMProvider{
		name:          name,
		baseURL:       baseURL,
		apiKey:        apiKey,
		httpClient:    &http.Client{},
		model:         model,
		temperature:   opts.Temperature,
		topP:          opts.TopP,
		repeatPenalty: opts.RepeatPenalty,
		maxTokens:     opts.MaxTokens,
	}
}

// Name returns the provider identifier.
func (p *VLLMProvider) Name() string {
	return p.name
}

// ChatStream sends messages with optional tools and returns a channel of streaming events.
func (p *VLLMProvider) ChatStream(ctx context.Context, messages []Message, tools []Tool) (<-chan StreamEvent, error) {
	req := vllmChatRequest{
		Model:             p.model,
		Messages:          mergeSystemMessagesOpenAI(toOpenAIMessages(messages)),
		Tools:             toOpenAITools(tools),
		Temperature:       float32(p.temperature),
		TopP:              float32(p.topP),
		RepetitionPenalty: float32(p.repeatPenalty),
		MaxTokens:         p.maxTokens,
		Stream:            true,
		StreamOptions:     &chatStreamOptions{IncludeUsage: true},
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	reader, err := httpDoSSE(ctx, httpRequestConfig{
		client:   p.httpClient,
		url:      p.baseURL + "/chat/completions",
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

// Close closes idle HTTP connections.
func (p *VLLMProvider) Close() error {
	if p.httpClient != nil {
		p.httpClient.CloseIdleConnections()
	}
	return nil
}

func (p *VLLMProvider) authHeaders() map[string]string {
	headers := make(map[string]string)
	if p.apiKey != "" {
		headers["Authorization"] = "Bearer " + p.apiKey
	}
	return headers
}
