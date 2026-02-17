package mcptools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xonecas/symb/internal/delta"
	"github.com/xonecas/symb/internal/lsp"
	"github.com/xonecas/symb/internal/mcp"
	"github.com/xonecas/symb/internal/provider"
	"github.com/xonecas/symb/internal/shell"
	"github.com/xonecas/symb/internal/store"
	"github.com/xonecas/symb/internal/subagent"
)

// SubAgentArgs represents arguments for the SubAgent tool.
type SubAgentArgs struct {
	Prompt        string `json:"prompt"`
	Type          string `json:"type,omitempty"`
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
				"type":           {"type": "string", "enum": ["explore", "editor", "reviewer", "web"], "description": "Subagent type. explore=read-only codebase search, editor=surgical code changes, reviewer=code review, web=documentation/API research. Omit for general tasks."},
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

	// Create isolated FileReadTracker for sub-agent
	subTracker := NewFileReadTracker()

	// Create fresh handlers with isolated tracker
	subReadHandler := NewReadHandler(subTracker, h.lspManager)
	subEditHandler := NewEditHandler(subTracker, h.lspManager, h.deltaTracker)
	subShellHandler := NewShellHandler(h.sh)

	// Create proxy with sub-agent tools (filtered - no nested SubAgent)
	subProxy := mcp.NewProxy(nil)
	filteredTools := subagent.FilterToolsForType(h.allTools, args.Type)

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

	result, err := subagent.Run(ctx, subagent.Options{
		Provider:      h.provider,
		Proxy:         subProxy,
		Tools:         filteredTools,
		Prompt:        args.Prompt,
		Type:          args.Type,
		MaxIterations: args.MaxIterations,
	})
	if err != nil {
		return toolError("%v", err), nil
	}

	output := fmt.Sprintf("Sub-agent completed.\n\n%s\n\n---\nToken usage: %d in, %d out",
		result.Content, result.InputTokens, result.OutputTokens)

	return toolText(output), nil
}
