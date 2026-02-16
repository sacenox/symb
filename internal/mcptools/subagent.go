package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/xonecas/symb/internal/delta"
	"github.com/xonecas/symb/internal/llm"
	"github.com/xonecas/symb/internal/lsp"
	"github.com/xonecas/symb/internal/mcp"
	"github.com/xonecas/symb/internal/provider"
	"github.com/xonecas/symb/internal/shell"
	"github.com/xonecas/symb/internal/store"
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

// SubAgentArgs represents arguments for the SubAgent tool.
type SubAgentArgs struct {
	Prompt        string `json:"prompt"`
	MaxIterations int    `json:"max_iterations,omitempty"`
}

// NewSubAgentTool creates the SubAgent tool definition.
func NewSubAgentTool() mcp.Tool {
	return mcp.Tool{
		Name:        "SubAgent",
		Description: `Spawn a sub-agent to handle a focused task. The sub-agent runs with the same tools but cannot spawn further sub-agents. Use this to decompose complex tasks into smaller, manageable pieces. The sub-agent's work is returned as a summary.`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"prompt":         {"type": "string", "description": "Task description for the sub-agent. Be specific about what needs to be accomplished and the expected output format."},
				"max_iterations": {"type": "integer", "description": "Maximum tool rounds for the sub-agent (default: 5)"}
			},
			"required": ["prompt"]
		}`),
	}
}

// SubAgentHandler handles SubAgent tool calls.
type SubAgentHandler struct {
	provider     provider.Provider
	lspManager   *lsp.Manager
	deltaTracker *delta.Tracker
	sh           *shell.Shell
	webCache     *store.Cache
	exaKey       string
	allTools     []mcp.Tool
}

// NewSubAgentHandler creates a handler for the SubAgent tool.
func NewSubAgentHandler(
	prov provider.Provider,
	lspManager *lsp.Manager,
	deltaTracker *delta.Tracker,
	sh *shell.Shell,
	webCache *store.Cache,
	exaKey string,
	allTools []mcp.Tool,
) *SubAgentHandler {
	// Validate required dependencies
	if prov == nil {
		panic("SubAgentHandler: provider cannot be nil")
	}
	if sh == nil {
		panic("SubAgentHandler: shell cannot be nil")
	}
	// lspManager, deltaTracker, webCache can be nil (handlers check internally)

	return &SubAgentHandler{
		provider:     prov,
		lspManager:   lspManager,
		deltaTracker: deltaTracker,
		sh:           sh,
		webCache:     webCache,
		exaKey:       exaKey,
		allTools:     allTools,
	}
}

// Handle implements the mcp.ToolHandler interface.
func (h *SubAgentHandler) Handle(ctx context.Context, arguments json.RawMessage) (*mcp.ToolResult, error) {
	// Check if context is already cancelled
	if err := ctx.Err(); err != nil {
		return toolError("Sub-agent cancelled: %v", err), nil
	}

	var args SubAgentArgs
	if err := json.Unmarshal(arguments, &args); err != nil {
		return toolError("Invalid arguments: %v", err), nil
	}
	if args.Prompt == "" {
		return toolError("prompt is required"), nil
	}

	maxIter := MaxSubAgentIterations
	if args.MaxIterations > 0 {
		if args.MaxIterations > MaxAllowedIterations {
			return toolError("max_iterations too large (max: %d)", MaxAllowedIterations), nil
		}
		maxIter = args.MaxIterations
	}

	// Create isolated FileReadTracker for sub-agent
	subTracker := NewFileReadTracker()

	// Create fresh handlers with isolated tracker
	subReadHandler := NewReadHandler(subTracker, h.lspManager)
	subEditHandler := NewEditHandler(subTracker, h.lspManager, h.deltaTracker)
	subShellHandler := NewShellHandler(h.sh)

	// Create proxy with sub-agent tools (filtered - no nested SubAgent)
	subProxy := mcp.NewProxy(nil)
	filteredTools := filterSubAgentTool(h.allTools)

	// Register tools with sub-agent proxy
	for _, tool := range filteredTools {
		switch tool.Name {
		case "Read":
			subProxy.RegisterTool(tool, subReadHandler.Handle)
		case "Edit":
			subProxy.RegisterTool(tool, subEditHandler.Handle)
		case "Shell":
			subProxy.RegisterTool(tool, subShellHandler.Handle)
		case "Grep":
			subProxy.RegisterTool(tool, MakeGrepHandler())
		case "TodoWrite":
			// Sub-agents get their own scratchpad
			subPad := &Scratchpad{}
			subProxy.RegisterTool(tool, MakeTodoWriteHandler(subPad))
		case "WebFetch":
			subProxy.RegisterTool(tool, MakeWebFetchHandler(h.webCache))
		case "WebSearch":
			subProxy.RegisterTool(tool, MakeWebSearchHandler(h.webCache, h.exaKey, ""))
		}
	}

	// Build sub-agent history: system prompt + user task
	subHistory := []provider.Message{
		{
			Role:      "system",
			Content:   buildSubAgentSystemPrompt(),
			CreatedAt: time.Now(),
		},
		{
			Role:      "user",
			Content:   args.Prompt,
			CreatedAt: time.Now(),
		},
	}

	// Track sub-agent's token usage
	var totalIn, totalOut int

	// Collect sub-agent's messages for the result
	var subMessages []provider.Message

	// Run sub-agent turn
	err := llm.ProcessTurn(ctx, llm.ProcessTurnOptions{
		Provider: h.provider,
		Proxy:    subProxy,
		Tools:    filteredTools,
		History:  subHistory,
		OnMessage: func(msg provider.Message) {
			subMessages = append(subMessages, msg)
		},
		OnUsage: func(in, out int) {
			totalIn += in
			totalOut += out
		},
		MaxToolRounds: maxIter,
		Depth:         1, // Sub-agent is at depth 1
	})

	if err != nil {
		return toolError("Sub-agent failed: %v", err), nil
	}

	// Check if sub-agent produced a final response
	if len(subMessages) == 0 {
		return toolError("Sub-agent produced no output"), nil
	}

	// Extract final assistant message
	var finalContent string
	for i := len(subMessages) - 1; i >= 0; i-- {
		if subMessages[i].Role == "assistant" && subMessages[i].Content != "" {
			finalContent = subMessages[i].Content
			break
		}
	}

	if finalContent == "" {
		return toolError("Sub-agent produced no final response"), nil
	}

	// Build result with usage metadata
	result := fmt.Sprintf("Sub-agent completed.\n\n%s\n\n---\nToken usage: %d in, %d out",
		finalContent, totalIn, totalOut)

	return toolText(result), nil
}

// filterSubAgentTool removes the SubAgent tool from a tool list.
func filterSubAgentTool(tools []mcp.Tool) []mcp.Tool {
	filtered := make([]mcp.Tool, 0, len(tools))
	for _, t := range tools {
		if t.Name != "SubAgent" {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// buildSubAgentSystemPrompt returns the system prompt for sub-agents.
func buildSubAgentSystemPrompt() string {
	return strings.TrimSpace(`
You are a focused sub-agent working on a specific task assigned by a parent agent.

Your role:
- Complete the assigned task efficiently
- Use tools as needed (Read, Edit, Grep, Shell, etc.)
- Provide a clear, concise final response summarizing what you accomplished
- You cannot spawn further sub-agents

Output format:
- Use tools to gather information and make changes
- When done, respond with a summary of what was accomplished
- Be specific about any files modified, tests run, or issues found

You have a limited number of tool rounds - work efficiently.
`)
}
