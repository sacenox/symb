package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const roleSystem = "system"

type OllamaProvider struct {
	name        string
	baseURL     string
	httpClient  *http.Client
	model       string
	temperature float64
}

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

func (p *OllamaProvider) Name() string {
	return p.name
}

func (p *OllamaProvider) ChatStream(ctx context.Context, messages []Message, tools []Tool) (<-chan StreamEvent, error) {
	req := ollamaChatRequest{
		Model:         p.model,
		Messages:      mergeConsecutiveSystemMessages(toOllamaMessages(messages)),
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

func (p *OllamaProvider) ListModels(ctx context.Context) ([]Model, error) {
	baseURL := strings.TrimRight(p.baseURL, "/v1")
	url := baseURL + "/api/tags"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list models status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var listResp ollamaListResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, err
	}

	models := make([]Model, len(listResp.Models))
	for i, m := range listResp.Models {
		models[i] = Model{
			Name:       m.Name,
			Size:       m.Size,
			Digest:     m.Digest,
			ModifiedAt: m.ModifiedAt,
			Format:     m.Details.Format,
			Family:     m.Details.Family,
			ParamSize:  m.Details.ParamSize,
			QuantLevel: m.Details.QuantLevel,
		}
	}
	return models, nil
}

func (p *OllamaProvider) Close() error {
	if p.httpClient != nil {
		p.httpClient.CloseIdleConnections()
	}
	return nil
}

type ollamaListResponse struct {
	Models []ollamaModel `json:"models"`
}

type ollamaModel struct {
	Name       string             `json:"name"`
	Size       int64              `json:"size"`
	Digest     string             `json:"digest"`
	ModifiedAt time.Time          `json:"modified_at"`
	Details    ollamaModelDetails `json:"details"`
}

type ollamaModelDetails struct {
	Format     string `json:"format"`
	Family     string `json:"family"`
	ParamSize  string `json:"parameter_size"`
	QuantLevel string `json:"quantization_level"`
}

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

func mergeConsecutiveSystemMessages(messages []ollamaReqMessage) []ollamaReqMessage {
	if len(messages) == 0 {
		return messages
	}

	result := make([]ollamaReqMessage, 0, len(messages))
	var systemBuffer strings.Builder
	inSystemRun := false

	for i, msg := range messages {
		if msg.Role == roleSystem {
			if inSystemRun {
				systemBuffer.WriteString("\n\n")
			} else {
				inSystemRun = true
			}
			systemBuffer.WriteString(msg.Content)
		} else {
			if inSystemRun {
				result = append(result, ollamaReqMessage{
					Role:    roleSystem,
					Content: systemBuffer.String(),
				})
				systemBuffer.Reset()
				inSystemRun = false
			}
			result = append(result, msg)
		}

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
		Msg("Merged consecutive system messages")

	return result
}

type chatCompletionStreamResponse struct {
	Choices []chatCompletionStreamChoice `json:"choices"`
	Usage   *chatCompletionUsage         `json:"usage,omitempty"`
}

type chatCompletionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type chatStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatCompletionStreamChoice struct {
	Delta        chatCompletionStreamDelta `json:"delta"`
	FinishReason *string                   `json:"finish_reason"`
}

type chatCompletionStreamDelta struct {
	Role             string                   `json:"role,omitempty"`
	Content          string                   `json:"content,omitempty"`
	Reasoning        string                   `json:"reasoning,omitempty"`
	ReasoningContent string                   `json:"reasoning_content,omitempty"`
	ToolCalls        []chatCompletionToolCall `json:"tool_calls,omitempty"`
}

type chatCompletionToolCall struct {
	Index    int                    `json:"index"`
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function chatCompletionFunction `json:"function"`
}

type chatCompletionFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type httpRequestConfig struct {
	client   *http.Client
	url      string
	body     []byte
	headers  map[string]string
	provider string
	model    string
}

var sseRetryDelays = []time.Duration{5 * time.Second, 10 * time.Second, 15 * time.Second}

func httpDoSSE(ctx context.Context, cfg httpRequestConfig) (io.ReadCloser, error) {
	maxRetries := len(sseRetryDelays)
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := sseRetryWait(ctx, cfg, attempt); err != nil {
			return nil, err
		}

		body, err, retry := sseAttempt(ctx, cfg, attempt)
		if err != nil {
			return nil, err
		}
		if retry != nil {
			lastErr = retry
			continue
		}
		return body, nil
	}

	return nil, fmt.Errorf("SSE request failed after %d retries: %w", maxRetries, lastErr)
}

func sseRetryWait(ctx context.Context, cfg httpRequestConfig, attempt int) error {
	if attempt == 0 {
		log.Info().Str("provider", cfg.provider).Str("model", cfg.model).Msg("SSE stream request started")
		return nil
	}
	delay := sseRetryDelays[attempt-1]
	log.Warn().Str("provider", cfg.provider).Int("attempt", attempt).Dur("delay", delay).Msg("Retrying SSE connection after transient error")
	select {
	case <-time.After(delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func isTransientStatus(code int) bool {
	return code == 429 || code == 500 || code == 502 || code == 503 || code == 504
}

func sseAttempt(ctx context.Context, cfg httpRequestConfig, attempt int) (io.ReadCloser, error, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.url, bytes.NewReader(cfg.body))
	if err != nil {
		return nil, err, nil
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	for k, v := range cfg.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := cfg.client.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err, nil
		}
		return nil, nil, err
	}

	if isTransientStatus(resp.StatusCode) {
		payload, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		retryErr := fmt.Errorf("stream request status %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
		log.Warn().Str("provider", cfg.provider).Int("status", resp.StatusCode).Int("attempt", attempt+1).Msg("SSE retryable error")
		return nil, nil, retryErr
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("stream request status %d: %s", resp.StatusCode, strings.TrimSpace(string(payload))), nil
	}

	return resp.Body, nil, nil
}

func parseSSEStream(ctx context.Context, reader io.Reader, ch chan<- StreamEvent) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 512*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			trySend(ctx, ch, StreamEvent{Type: EventDone})
			return
		}

		var chunk chatCompletionStreamResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			log.Warn().Err(err).Str("data", data).Msg("Failed to parse SSE chunk")
			continue
		}
		if chunk.Usage != nil {
			trySend(ctx, ch, StreamEvent{
				Type:         EventUsage,
				InputTokens:  chunk.Usage.PromptTokens,
				OutputTokens: chunk.Usage.CompletionTokens,
			})
		}
		if len(chunk.Choices) == 0 {
			continue
		}

		if !emitDelta(ctx, ch, chunk.Choices[0].Delta) {
			return
		}
	}

	if err := scanner.Err(); err != nil {
		trySend(ctx, ch, StreamEvent{Type: EventError, Err: err})
		return
	}
	trySend(ctx, ch, StreamEvent{Type: EventDone})
}

func emitDelta(ctx context.Context, ch chan<- StreamEvent, delta chatCompletionStreamDelta) bool {
	reasoning := delta.Reasoning
	if reasoning == "" {
		reasoning = delta.ReasoningContent
	}
	if reasoning != "" {
		if !trySend(ctx, ch, StreamEvent{Type: EventReasoningDelta, Content: reasoning}) {
			return false
		}
	}
	if delta.Content != "" {
		if !trySend(ctx, ch, StreamEvent{Type: EventContentDelta, Content: delta.Content}) {
			return false
		}
	}
	for _, tc := range delta.ToolCalls {
		if tc.Function.Name != "" {
			if !trySend(ctx, ch, StreamEvent{
				Type: EventToolCallBegin, ToolCallIndex: tc.Index,
				ToolCallID: tc.ID, ToolCallName: tc.Function.Name,
			}) {
				return false
			}
		}
		if tc.Function.Arguments != "" {
			if !trySend(ctx, ch, StreamEvent{
				Type: EventToolCallDelta, ToolCallIndex: tc.Index,
				ToolCallArgs: tc.Function.Arguments,
			}) {
				return false
			}
		}
	}
	return true
}

func trySend(ctx context.Context, ch chan<- StreamEvent, evt StreamEvent) bool {
	select {
	case ch <- evt:
		return true
	case <-ctx.Done():
		return false
	}
}
