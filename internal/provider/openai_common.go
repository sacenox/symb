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
	openai "github.com/sashabaranov/go-openai"
)

// SSE streaming delta types for the OpenAI Chat Completions streaming format.

type chatCompletionStreamResponse struct {
	Choices []chatCompletionStreamChoice `json:"choices"`
	Usage   *chatCompletionUsage         `json:"usage,omitempty"`
}

// chatCompletionUsage carries token counts from the final streaming chunk.
type chatCompletionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// chatStreamOptions requests usage info in the streaming response.
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

// httpRequestConfig holds the parameters for an HTTP SSE request.
type httpRequestConfig struct {
	client   *http.Client
	url      string
	body     []byte
	headers  map[string]string
	provider string // for logging
	model    string // for logging
}

// sseRetryDelays defines backoff for transient errors on the initial SSE connection.
var sseRetryDelays = []time.Duration{5 * time.Second, 10 * time.Second, 15 * time.Second}

// httpDoSSE executes an HTTP POST for SSE streaming with retry on the initial
// connection. Returns the response body as an io.ReadCloser that the caller
// must close.
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

// sseRetryWait sleeps with backoff between retry attempts. Returns ctx.Err() if cancelled.
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

// isTransientStatus returns true for HTTP status codes that should trigger a retry.
func isTransientStatus(code int) bool {
	return code == 429 || code == 500 || code == 502 || code == 503 || code == 504
}

// sseAttempt makes one HTTP request. Returns (body, nil, nil) on success,
// (nil, err, nil) on fatal error, or (nil, nil, retryErr) on transient error.
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
		return nil, nil, err // retryable
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

// parseSSEStream reads SSE lines from a reader and sends parsed stream events on the channel.
// Returns when the stream ends, an error occurs, or ctx is cancelled.
// Caller must close the reader.
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

		if !emitOpenAIDelta(ctx, ch, chunk.Choices[0].Delta) {
			return
		}
	}

	if err := scanner.Err(); err != nil {
		trySend(ctx, ch, StreamEvent{Type: EventError, Err: err})
		return
	}
	trySend(ctx, ch, StreamEvent{Type: EventDone})
}

// emitOpenAIDelta sends stream events for one OpenAI delta. Returns false if ctx cancelled.
func emitOpenAIDelta(ctx context.Context, ch chan<- StreamEvent, delta chatCompletionStreamDelta) bool {
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

// trySend sends an event on ch, aborting if ctx is cancelled. Returns false if cancelled.
func trySend(ctx context.Context, ch chan<- StreamEvent, evt StreamEvent) bool {
	select {
	case ch <- evt:
		return true
	case <-ctx.Done():
		return false
	}
}

// toOpenAIMessages converts provider-agnostic messages to OpenAI SDK message format.
func toOpenAIMessages(messages []Message) []openai.ChatCompletionMessage {
	result := make([]openai.ChatCompletionMessage, len(messages))
	for i, m := range messages {
		msg := openai.ChatCompletionMessage{
			Role:    m.Role,
			Content: m.Content,
		}

		// Handle tool call results
		if m.ToolCallID != "" {
			msg.ToolCallID = m.ToolCallID
		}

		// Handle assistant messages with tool calls
		if len(m.ToolCalls) > 0 {
			msg.ToolCalls = make([]openai.ToolCall, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				msg.ToolCalls[j] = openai.ToolCall{
					ID:   tc.ID,
					Type: openai.ToolTypeFunction,
					Function: openai.FunctionCall{
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

// mergeSystemMessagesOpenAI merges system messages intelligently while preserving conversation flow.
func mergeSystemMessagesOpenAI(messages []openai.ChatCompletionMessage) []openai.ChatCompletionMessage {
	if len(messages) == 0 {
		return messages
	}

	var systemMessages []string
	var conversationMessages []openai.ChatCompletionMessage

	for _, msg := range messages {
		if msg.Role == roleSystem {
			systemMessages = append(systemMessages, msg.Content)
		} else {
			conversationMessages = append(conversationMessages, msg)
		}
	}

	result := make([]openai.ChatCompletionMessage, 0, len(messages))

	if len(systemMessages) > 0 {
		mergedSystem := strings.Join(systemMessages, "\n\n")
		result = append(result, openai.ChatCompletionMessage{
			Role:    roleSystem,
			Content: mergedSystem,
		})
	}

	result = append(result, conversationMessages...)

	log.Debug().
		Int("original_count", len(messages)).
		Int("merged_count", len(result)).
		Int("system_merged", len(systemMessages)).
		Int("conversation_kept", len(conversationMessages)).
		Msg("OpenAI: Merged system messages")

	return result
}

// toOpenAITools converts provider-agnostic tools to OpenAI SDK tool format.
// Parameters is passed through as json.RawMessage to preserve deterministic
// serialization order (important for KV-cache hit rate).
func toOpenAITools(tools []Tool) []openai.Tool {
	if tools == nil {
		return nil
	}
	emptyParams := json.RawMessage(`{"type":"object","properties":{}}`)
	result := make([]openai.Tool, len(tools))
	for i, t := range tools {
		params := t.Parameters
		if len(params) == 0 {
			params = emptyParams
		}

		result[i] = openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		}
	}
	return result
}
