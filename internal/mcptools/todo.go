package mcptools

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/xonecas/symb/internal/mcp"
)

// Scratchpad holds the agent's current plan/notes. It is safe for concurrent
// access. The content is injected into the LLM context at the tail of the
// history so the agent's goals stay in the model's recent attention window.
type Scratchpad struct {
	mu      sync.RWMutex
	content string
}

// Content returns the current scratchpad text.
func (s *Scratchpad) Content() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.content
}

// TodoWriteArgs represents arguments for the TodoWrite tool.
type TodoWriteArgs struct {
	Content string `json:"content"`
}

// NewTodoWriteTool creates the TodoWrite tool definition.
func NewTodoWriteTool() mcp.Tool {
	return mcp.Tool{
		Name:        "TodoWrite",
		Description: `Write or update your working plan/scratchpad. The content replaces any previous plan and is kept visible at the end of your context window. Use this to track goals, progress, and next steps for tasks with 3+ steps. Rewrite it as you complete steps to stay focused. Skip for simple single-step tasks.`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"content": {"type": "string", "description": "Your current plan, todo list, or working notes. This replaces the previous content entirely."}
			},
			"required": ["content"]
		}`),
	}
}

// MakeTodoWriteHandler creates a handler that stores content in the scratchpad.
func MakeTodoWriteHandler(pad *Scratchpad) mcp.ToolHandler {
	return func(_ context.Context, arguments json.RawMessage) (*mcp.ToolResult, error) {
		var args TodoWriteArgs
		if err := json.Unmarshal(arguments, &args); err != nil {
			return &mcp.ToolResult{
				Content: []mcp.ContentBlock{{Type: "text", Text: "Invalid arguments: " + err.Error()}},
				IsError: true,
			}, nil
		}
		if args.Content == "" {
			return &mcp.ToolResult{
				Content: []mcp.ContentBlock{{Type: "text", Text: "Content cannot be empty"}},
				IsError: true,
			}, nil
		}

		pad.mu.Lock()
		pad.content = args.Content
		pad.mu.Unlock()

		return &mcp.ToolResult{
			Content: []mcp.ContentBlock{{Type: "text", Text: "Plan updated."}},
		}, nil
	}
}
