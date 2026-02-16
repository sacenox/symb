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

// --- OpenAI Responses API types ---

// responsesRequest is the request body for POST /responses.
type responsesRequest struct {
	Model       string               `json:"model"`
	Input       []responsesInputItem `json:"input"`
	Tools       []responsesToolParam `json:"tools,omitempty"`
	Temperature *float32             `json:"temperature,omitempty"`
	Stream      bool                 `json:"stream"`
}

// responsesInputItem is a polymorphic input item (message or function_call_output).
type responsesInputItem struct {
	Type    string `json:"type"`              // "message", "function_call", or "function_call_output"
	Role    string `json:"role,omitempty"`    // for messages: "system", "user", "assistant", "developer"
	Content any    `json:"content,omitempty"` // string or []responsesContentPart
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	// function_call fields
	Arguments string `json:"arguments,omitempty"`
	// function_call_output fields
	CallID string `json:"call_id,omitempty"`
	Output string `json:"output,omitempty"`
}

// responsesToolParam defines a function tool for the Responses API.
type responsesToolParam struct {
	Type        string          `json:"type"` // "function"
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// SSE event types we care about from the Responses API stream.
type responsesOutputTextDelta struct {
	Delta string `json:"delta"`
}

type responsesOutputItemAdded struct {
	OutputIndex int                     `json:"output_index"`
	Item        responsesOutputItemInfo `json:"item"`
}

type responsesOutputItemInfo struct {
	ID     string `json:"id"`
	Type   string `json:"type"` // "message", "function_call", "reasoning"
	Name   string `json:"name,omitempty"`
	CallID string `json:"call_id,omitempty"`
}

type responsesFuncCallArgsDelta struct {
	OutputIndex int    `json:"output_index"`
	Delta       string `json:"delta"`
}

type responsesReasoningDelta struct {
	Delta string `json:"delta"`
}

type responsesCompleted struct {
	Response struct {
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage,omitempty"`
	} `json:"response"`
}

type responsesFailed struct {
	Response struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"response"`
}

// toResponsesInput converts provider-agnostic messages to Responses API input items.
func toResponsesInput(messages []Message) []responsesInputItem {
	var items []responsesInputItem
	for _, m := range messages {
		switch m.Role {
		case "tool":
			items = append(items, responsesInputItem{
				Type:   "function_call_output",
				CallID: m.ToolCallID,
				Output: m.Content,
			})
		case "assistant":
			if len(m.ToolCalls) > 0 {
				// Emit assistant text as a message, then each tool call as a function_call item.
				if m.Content != "" {
					items = append(items, responsesInputItem{
						Type:    "message",
						Role:    "assistant",
						Content: m.Content,
					})
				}
				for _, tc := range m.ToolCalls {
					items = append(items, responsesInputItem{
						Type:      "function_call",
						CallID:    tc.ID,
						Name:      tc.Name,
						Arguments: string(tc.Arguments),
					})
				}
				continue
			}
			items = append(items, responsesInputItem{
				Type:    "message",
				Role:    "assistant",
				Content: m.Content,
			})
		case roleSystem:
			items = append(items, responsesInputItem{
				Type:    "message",
				Role:    "developer",
				Content: m.Content,
			})
		default:
			items = append(items, responsesInputItem{
				Type:    "message",
				Role:    m.Role,
				Content: m.Content,
			})
		}
	}
	return items
}

// toResponsesTools converts provider-agnostic tools to Responses API tool format.
func toResponsesTools(tools []Tool) []responsesToolParam {
	if tools == nil {
		return nil
	}
	emptyParams := json.RawMessage(`{"type":"object","properties":{}}`)
	result := make([]responsesToolParam, len(tools))
	for i, t := range tools {
		params := t.Parameters
		if len(params) == 0 {
			params = emptyParams
		}
		result[i] = responsesToolParam{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  params,
		}
	}
	return result
}

// responsesTracker maps Responses API output indices to sequential tool call indices.
type responsesTracker struct {
	toolCallCount   int
	outputToToolIdx map[int]int
}

func newResponsesTracker() *responsesTracker {
	return &responsesTracker{outputToToolIdx: make(map[int]int)}
}

// parseResponsesSSEStream reads Responses API SSE events and emits StreamEvents.
//
// The Responses API SSE format uses typed events:
//
//	event: response.output_text.delta
//	data: {"delta":"hello"}
func parseResponsesSSEStream(ctx context.Context, reader io.Reader, ch chan<- StreamEvent) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 512*1024)

	rt := newResponsesTracker()
	var currentEventType string

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "event: ") {
			currentEventType = strings.TrimPrefix(line, "event: ")
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		done, stop := rt.handleResponsesEvent(ctx, ch, currentEventType, data)
		if done || stop {
			return
		}
		currentEventType = ""
	}

	if err := scanner.Err(); err != nil {
		trySend(ctx, ch, StreamEvent{Type: EventError, Err: err})
		return
	}
	trySend(ctx, ch, StreamEvent{Type: EventDone})
}

// handleResponsesEvent dispatches a single Responses API SSE event.
// Returns (done, stop): done=true means the stream ended normally or with error,
// stop=true means ctx was cancelled.
func (rt *responsesTracker) handleResponsesEvent(ctx context.Context, ch chan<- StreamEvent, eventType, data string) (bool, bool) {
	switch eventType {
	case "response.output_text.delta":
		return false, !rt.handleTextDelta(ctx, ch, data)
	case "response.reasoning_summary_text.delta":
		return false, !rt.handleReasoningDelta(ctx, ch, data)
	case "response.output_item.added":
		return false, !rt.handleOutputItemAdded(ctx, ch, data)
	case "response.function_call_arguments.delta":
		return false, !rt.handleFuncCallDelta(ctx, ch, data)
	case "response.completed":
		rt.handleCompleted(ctx, ch, data)
		return true, false
	case "response.failed":
		rt.handleFailed(ctx, ch, data)
		return true, false
	case "response.incomplete":
		trySend(ctx, ch, StreamEvent{Type: EventDone})
		return true, false
	}
	return false, false
}

func (rt *responsesTracker) handleTextDelta(ctx context.Context, ch chan<- StreamEvent, data string) bool {
	var evt responsesOutputTextDelta
	if err := json.Unmarshal([]byte(data), &evt); err != nil {
		log.Warn().Err(err).Msg("Failed to parse responses output_text.delta")
		return true
	}
	if evt.Delta != "" {
		return trySend(ctx, ch, StreamEvent{Type: EventContentDelta, Content: evt.Delta})
	}
	return true
}

func (rt *responsesTracker) handleReasoningDelta(ctx context.Context, ch chan<- StreamEvent, data string) bool {
	var evt responsesReasoningDelta
	if err := json.Unmarshal([]byte(data), &evt); err != nil {
		log.Warn().Err(err).Msg("Failed to parse responses reasoning delta")
		return true
	}
	if evt.Delta != "" {
		return trySend(ctx, ch, StreamEvent{Type: EventReasoningDelta, Content: evt.Delta})
	}
	return true
}

func (rt *responsesTracker) handleOutputItemAdded(ctx context.Context, ch chan<- StreamEvent, data string) bool {
	var evt responsesOutputItemAdded
	if err := json.Unmarshal([]byte(data), &evt); err != nil {
		log.Warn().Err(err).Msg("Failed to parse responses output_item.added")
		return true
	}
	if evt.Item.Type == "function_call" {
		idx := rt.toolCallCount
		rt.toolCallCount++
		rt.outputToToolIdx[evt.OutputIndex] = idx
		return trySend(ctx, ch, StreamEvent{
			Type:          EventToolCallBegin,
			ToolCallIndex: idx,
			ToolCallID:    evt.Item.CallID,
			ToolCallName:  evt.Item.Name,
		})
	}
	return true
}

func (rt *responsesTracker) handleFuncCallDelta(ctx context.Context, ch chan<- StreamEvent, data string) bool {
	var evt responsesFuncCallArgsDelta
	if err := json.Unmarshal([]byte(data), &evt); err != nil {
		log.Warn().Err(err).Msg("Failed to parse responses function_call_arguments.delta")
		return true
	}
	if evt.Delta != "" {
		idx := rt.outputToToolIdx[evt.OutputIndex]
		return trySend(ctx, ch, StreamEvent{
			Type:          EventToolCallDelta,
			ToolCallIndex: idx,
			ToolCallArgs:  evt.Delta,
		})
	}
	return true
}

func (rt *responsesTracker) handleCompleted(ctx context.Context, ch chan<- StreamEvent, data string) {
	var evt responsesCompleted
	if err := json.Unmarshal([]byte(data), &evt); err != nil {
		log.Warn().Err(err).Msg("Failed to parse responses completed")
		trySend(ctx, ch, StreamEvent{Type: EventDone})
		return
	}
	if evt.Response.Usage != nil {
		trySend(ctx, ch, StreamEvent{
			Type:         EventUsage,
			InputTokens:  evt.Response.Usage.InputTokens,
			OutputTokens: evt.Response.Usage.OutputTokens,
		})
	}
	trySend(ctx, ch, StreamEvent{Type: EventDone})
}

func (rt *responsesTracker) handleFailed(ctx context.Context, ch chan<- StreamEvent, data string) {
	var evt responsesFailed
	if err := json.Unmarshal([]byte(data), &evt); err != nil {
		trySend(ctx, ch, StreamEvent{Type: EventError, Err: fmt.Errorf("responses stream failed")})
		return
	}
	trySend(ctx, ch, StreamEvent{
		Type: EventError,
		Err:  fmt.Errorf("responses API error %s: %s", evt.Response.Error.Code, evt.Response.Error.Message),
	})
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
