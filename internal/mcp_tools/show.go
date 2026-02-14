package mcp_tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"github.com/xonecas/symb/internal/mcp"
)

// ShowArgs represents arguments for the Show tool.
type ShowArgs struct {
	Content  string `json:"content,omitempty"`
	Language string `json:"language,omitempty"`
	FilePath string `json:"file_path,omitempty"`
}

// NewShowTool creates the Show tool definition.
func NewShowTool() mcp.Tool {
	return mcp.Tool{
		Name:        "Show",
		Description: `Displays content in the user's editor pane with syntax highlighting. Provide either "content" or "file_path", not both. Use file_path to show a file from disk (saves tokens). Use content for generated snippets or diffs.`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"content":   {"type": "string", "description": "The content to display. Mutually exclusive with file_path."},
				"language":  {"type": "string", "description": "Language for syntax highlighting (e.g. go, python, diff, markdown). Defaults to auto-detect for file_path, text for content."},
				"file_path": {"type": "string", "description": "Path to a file to display. Reads from disk. Enables git gutter markers and LSP diagnostics. Mutually exclusive with content."}
			},
			"oneOf": [
				{"required": ["content"]},
				{"required": ["file_path"]}
			]
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

	if args.Content != "" && args.FilePath != "" {
		return &mcp.ToolResult{
			Content: []mcp.ContentBlock{{Type: "text", Text: "Provide either content or file_path, not both"}},
			IsError: true,
		}, nil
	}
	if args.Content == "" && args.FilePath == "" {
		return &mcp.ToolResult{
			Content: []mcp.ContentBlock{{Type: "text", Text: "Provide either content or file_path"}},
			IsError: true,
		}, nil
	}

	content := args.Content
	language := args.Language
	var absPath string

	if args.FilePath != "" {
		absPath, _ = filepath.Abs(args.FilePath)
		data, err := os.ReadFile(absPath)
		if err != nil {
			return &mcp.ToolResult{
				Content: []mcp.ContentBlock{{Type: "text", Text: fmt.Sprintf("Cannot read file: %v", err)}},
				IsError: true,
			}, nil
		}
		content = string(data)
		if language == "" {
			language = DetectLanguage(args.FilePath)
		}
	}

	if language == "" {
		language = "text"
	}

	if h.program != nil {
		h.program.Send(ShowMsg{
			Content:  content,
			Language: language,
			FilePath: args.FilePath,
			AbsPath:  absPath,
		})
	}

	return &mcp.ToolResult{
		Content: []mcp.ContentBlock{{Type: "text", Text: fmt.Sprintf("Displayed content in editor (%s)", language)}},
	}, nil
}
