package subagent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xonecas/symb/internal/llm"
	"github.com/xonecas/symb/internal/mcp"
	"github.com/xonecas/symb/internal/provider"
)

const (
	// MaxSubAgentDepth is the maximum recursion depth for sub-agents.
	// Depth 0 = root agent, depth 1 = sub-agent spawned by root.
	MaxSubAgentDepth = 1

	// MaxSubAgentIterations is the default max tool rounds for sub-agents.
	MaxSubAgentIterations = 5

	// MaxAllowedIterations is the upper bound for user-specified max_iterations.
	MaxAllowedIterations = 20
)

// Options configures a sub-agent run.
type Options struct {
	Provider      provider.Provider
	Proxy         *mcp.Proxy
	Tools         []mcp.Tool
	Prompt        string
	MaxIterations int
}

// Result reports a sub-agent run outcome.
type Result struct {
	Content      string
	InputTokens  int
	OutputTokens int
}

// Run executes a sub-agent turn and returns the final assistant content.
func Run(ctx context.Context, opts Options) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("sub-agent cancelled: %v", err)
	}
	if opts.Provider == nil {
		return Result{}, fmt.Errorf("provider is required")
	}
	if opts.Proxy == nil {
		return Result{}, fmt.Errorf("proxy is required")
	}
	if opts.Prompt == "" {
		return Result{}, fmt.Errorf("prompt is required")
	}

	maxIter := MaxSubAgentIterations
	if opts.MaxIterations > 0 {
		if opts.MaxIterations > MaxAllowedIterations {
			return Result{}, fmt.Errorf("max_iterations too large (max: %d)", MaxAllowedIterations)
		}
		maxIter = opts.MaxIterations
	}

	subHistory := []provider.Message{
		{
			Role:      "system",
			Content:   SystemPrompt(),
			CreatedAt: time.Now(),
		},
		{
			Role:      "user",
			Content:   opts.Prompt,
			CreatedAt: time.Now(),
		},
	}

	var totalIn, totalOut int
	var subMessages []provider.Message

	err := llm.ProcessTurn(ctx, llm.ProcessTurnOptions{
		Provider: opts.Provider,
		Proxy:    opts.Proxy,
		Tools:    opts.Tools,
		History:  subHistory,
		OnMessage: func(msg provider.Message) {
			subMessages = append(subMessages, msg)
		},
		OnUsage: func(in, out int) {
			totalIn += in
			totalOut += out
		},
		MaxToolRounds: maxIter,
		Depth:         MaxSubAgentDepth,
	})
	if err != nil {
		return Result{}, fmt.Errorf("sub-agent failed: %v", err)
	}

	if len(subMessages) == 0 {
		return Result{}, fmt.Errorf("sub-agent produced no output")
	}

	var finalContent string
	for i := len(subMessages) - 1; i >= 0; i-- {
		if subMessages[i].Role == "assistant" && subMessages[i].Content != "" {
			finalContent = subMessages[i].Content
			break
		}
	}

	if finalContent == "" {
		return Result{}, fmt.Errorf("sub-agent produced no final response")
	}

	return Result{Content: finalContent, InputTokens: totalIn, OutputTokens: totalOut}, nil
}

// FilterTools removes the SubAgent tool from a tool list.
func FilterTools(tools []mcp.Tool) []mcp.Tool {
	filtered := make([]mcp.Tool, 0, len(tools))
	for _, t := range tools {
		if t.Name != "SubAgent" {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// SystemPrompt returns the system prompt for sub-agents.
func SystemPrompt() string {
	parts := []string{
		llm.SubAgentBasePrompt(),
		llm.SubAgentPrompt(),
	}
	if instructions := llm.LoadAgentInstructions(); instructions != "" {
		parts = append(parts, instructions)
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n---\n\n"))
}
