// Package mcp implements Model Context Protocol client and proxy functionality.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// ToolHandler is a function that handles a tool call.
type ToolHandler func(ctx context.Context, arguments json.RawMessage) (*ToolResult, error)

// Proxy combines an upstream MCP client with local tool handlers.
type Proxy struct {
	mu            sync.RWMutex
	upstream      UpstreamClient
	localTools    map[string]Tool
	localHandlers map[string]ToolHandler
}

var (
	ErrToolRetryExhausted = errors.New("mcp tool call failed after retries")
)

// Retry delays increased to respect SpaceMolt's "Try again in 5 seconds" rate limit
var toolRetryDelays = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}

// retryAfterRegex matches "Retry-After: N" in error messages
var retryAfterRegex = regexp.MustCompile(`Retry-After:\s*(\d+)`)

// parseRetryAfter extracts retry delay from error message containing "Retry-After: N seconds"
func parseRetryAfter(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}

	errMsg := err.Error()

	// Try to extract from "Retry-After: N" header format
	if matches := retryAfterRegex.FindStringSubmatch(errMsg); len(matches) > 1 {
		if seconds, parseErr := strconv.Atoi(matches[1]); parseErr == nil {
			return time.Duration(seconds) * time.Second, true
		}
	}

	// Try to extract from "Try again in N seconds" message format
	if strings.Contains(errMsg, "Try again in") {
		re := regexp.MustCompile(`Try again in (\d+) seconds?`)
		if matches := re.FindStringSubmatch(errMsg); len(matches) > 1 {
			if seconds, parseErr := strconv.Atoi(matches[1]); parseErr == nil {
				return time.Duration(seconds) * time.Second, true
			}
		}
	}

	return 0, false
}

// NewProxy creates a new MCP proxy.
func NewProxy(upstream UpstreamClient) *Proxy {
	return &Proxy{
		upstream:      upstream,
		localTools:    make(map[string]Tool),
		localHandlers: make(map[string]ToolHandler),
	}
}

// RegisterTool registers a local tool with the proxy.
func (p *Proxy) RegisterTool(tool Tool, handler ToolHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.localTools[tool.Name] = tool
	p.localHandlers[tool.Name] = handler
}

// ListTools returns all available tools (local + upstream).
func (p *Proxy) ListTools(ctx context.Context) ([]Tool, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Start with local tools
	tools := make([]Tool, 0, len(p.localTools))
	for _, t := range p.localTools {
		tools = append(tools, t)
	}

	// Add upstream tools if available
	if p.upstream != nil {
		upstreamTools, err := p.upstream.ListTools(ctx)
		if err != nil {
			log.Warn().
				Err(err).
				Msg("failed to list upstream tools")
		} else {
			tools = append(tools, upstreamTools...)
		}
	}

	return tools, nil
}

// CallTool invokes a tool, checking local handlers first then upstream.
func (p *Proxy) CallTool(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
	p.mu.RLock()
	handler, isLocal := p.localHandlers[name]
	p.mu.RUnlock()

	// Try local handler first
	if isLocal {
		return handler(ctx, arguments)
	}

	// Fall back to upstream
	if p.upstream != nil {
		var args interface{}
		if len(arguments) > 0 {
			if err := json.Unmarshal(arguments, &args); err != nil {
				return nil, fmt.Errorf("unmarshal arguments: %w", err)
			}
		}

		return p.callUpstreamWithRetry(ctx, name, args)
	}

	errorMsg := fmt.Sprintf("tool not found: %s", name)
	return &ToolResult{
		Content: []ContentBlock{{Type: "text", Text: errorMsg}},
		IsError: true,
	}, nil
}

func (p *Proxy) callUpstreamWithRetry(ctx context.Context, name string, args interface{}) (*ToolResult, error) {
	var lastErr error
	for attempt := 0; attempt <= len(toolRetryDelays); attempt++ {
		if attempt > 0 {
			delay := toolRetryDelays[attempt-1]

			// Check if error is a 429 rate limit
			is429 := lastErr != nil && (strings.Contains(lastErr.Error(), "429") || strings.Contains(lastErr.Error(), "Rate limited"))

			// Try to parse Retry-After from error message
			if retryAfter, ok := parseRetryAfter(lastErr); ok {
				// Use server-specified delay, but cap at 30 seconds for safety
				if retryAfter > 30*time.Second {
					retryAfter = 30 * time.Second
				}
				delay = retryAfter

				log.Warn().
					Str("tool", name).
					Int("attempt", attempt).
					Dur("delay", delay).
					Dur("server_requested", retryAfter).
					Err(lastErr).
					Msg("MCP tool rate limited - respecting Retry-After")
			} else if is429 {
				log.Warn().
					Str("tool", name).
					Int("attempt", attempt).
					Dur("delay", delay).
					Err(lastErr).
					Msg("MCP tool rate limited - waiting before retry")
			} else {
				log.Warn().
					Str("tool", name).
					Int("attempt", attempt).
					Dur("delay", delay).
					Msg("Retrying MCP tool call after error")
			}

			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		result, err := p.upstream.CallTool(ctx, name, args)
		if err == nil {
			// Log successful call at Info level for visibility
			if attempt > 0 {
				log.Info().
					Str("tool", name).
					Int("attempt", attempt+1).
					Msg("MCP tool call succeeded after retry")
			}
			return result, nil
		}

		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}

		lastErr = err
	}

	// Log final failure with more context
	log.Error().
		Str("tool", name).
		Int("total_attempts", len(toolRetryDelays)+1).
		Err(lastErr).
		Msg("MCP tool call failed after all retries")

	return nil, fmt.Errorf("%w: %v", ErrToolRetryExhausted, lastErr)
}

// Initialize initializes the upstream connection if available.
func (p *Proxy) Initialize(ctx context.Context) error {
	if p.upstream == nil {
		return nil
	}

	clientInfo := map[string]interface{}{
		"name":    "symb",
		"version": "0.1.0",
	}

	resp, err := p.upstream.Initialize(ctx, clientInfo)
	if err != nil {
		return fmt.Errorf("initialize upstream: %w", err)
	}

	if resp.Error != nil {
		return fmt.Errorf("upstream error: %s", resp.Error.Message)
	}

	return nil
}

// HasUpstream returns true if an upstream client is configured.
func (p *Proxy) HasUpstream() bool {
	return p.upstream != nil
}

// LocalToolCount returns the number of registered local tools.
func (p *Proxy) LocalToolCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.localTools)
}

// Close closes the upstream client connection if available.
func (p *Proxy) Close() error {
	p.mu.RLock()
	upstream := p.upstream
	p.mu.RUnlock()

	if upstream != nil {
		if closer, ok := upstream.(interface{ Close() error }); ok {
			return closer.Close()
		}
	}
	return nil
}
