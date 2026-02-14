package mcp_tools

import (
	"context"
	"encoding/json"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/xonecas/symb/internal/mcp"
)

// ShowArgs represents arguments for the Show tool.
type ShowArgs struct {
	Content  string `json:"content"`
	Language string `json:"language,omitempty"`
}

// NewShowTool creates the Show tool definition.
func NewShowTool() mcp.Tool {
	return mcp.Tool{
		Name:        "Show",
		Description: `Displays content in the user's editor pane with syntax highlighting. Use this to show code snippets, diffs, generated code, or any text the user should see. Does NOT read or write files.`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"content":  {"type": "string", "description": "The content to display in the editor pane"},
				"language": {"type": "string", "description": "Language for syntax highlighting (e.g. go, python, diff, markdown). Defaults to text."}
			},
			"required": ["content"]
		}`),
	}
}

// ShowHandler handles Show tool calls and sends content to the TUI editor.
type ShowHandler struct {
	program *tea.Program
}

// NewShowHandler creates a handler for the Show tool.
func NewShowHandler() *ShowHandler {
	return &ShowHandler{}
}

// SetProgram sets the tea.Program instance after it's created.
func (h *ShowHandler) SetProgram(program *tea.Program) {
	h.program = program
}

// Handle implements the mcp.ToolHandler interface.
func (h *ShowHandler) Handle(_ context.Context, arguments json.RawMessage) (*mcp.ToolResult, error) {
	var args ShowArgs
	if err := json.Unmarshal(arguments, &args); err != nil {
		return &mcp.ToolResult{
			Content: []mcp.ContentBlock{{Type: "text", Text: fmt.Sprintf("Invalid arguments: %v", err)}},
			IsError: true,
		}, nil
	}

	if args.Content == "" {
		return &mcp.ToolResult{
			Content: []mcp.ContentBlock{{Type: "text", Text: "Content cannot be empty"}},
			IsError: true,
		}, nil
	}

	language := args.Language
	if language == "" {
		language = "text"
	}

	if h.program != nil {
		h.program.Send(ShowMsg{
			Content:  args.Content,
			Language: language,
		})
	}

	return &mcp.ToolResult{
		Content: []mcp.ContentBlock{{Type: "text", Text: fmt.Sprintf("Displayed content in editor (%s)", language)}},
	}, nil
}
