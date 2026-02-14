package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xonecas/symb/internal/hashline"
	"github.com/xonecas/symb/internal/lsp"
	"github.com/xonecas/symb/internal/mcp"
	"github.com/xonecas/symb/internal/treesitter"
)

// ReadArgs represents arguments for the Read tool.
type ReadArgs struct {
	File  string `json:"file"`
	Start int    `json:"start,omitempty"` // Optional: start line (1-indexed)
	End   int    `json:"end,omitempty"`   // Optional: end line (1-indexed)
}

// NewReadTool creates the Read tool definition.
func NewReadTool() mcp.Tool {
	return mcp.Tool{
		Name:        "Read",
		Description: `Reads a file and returns hashline-tagged content. Each line is returned as "linenum:hash|content". You MUST Read a file before editing it with Edit. Use start/end for line ranges.`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file":  {"type": "string", "description": "Path to the file to read"},
				"start": {"type": "integer", "description": "Optional: starting line number (1-indexed, inclusive)"},
				"end":   {"type": "integer", "description": "Optional: ending line number (1-indexed, inclusive)"}
			},
			"required": ["file"]
		}`),
	}
}

// ReadHandler handles Read tool calls.
type ReadHandler struct {
	tracker    *FileReadTracker
	lspManager *lsp.Manager
	tsIndex    *treesitter.Index
}

// NewReadHandler creates a handler for the Read tool.
func NewReadHandler(tracker *FileReadTracker, lspManager *lsp.Manager) *ReadHandler {
	return &ReadHandler{tracker: tracker, lspManager: lspManager}
}

// SetTSIndex sets the tree-sitter index for incremental updates on read.
func (h *ReadHandler) SetTSIndex(idx *treesitter.Index) { h.tsIndex = idx }

// Handle implements the mcp.ToolHandler interface.
func (h *ReadHandler) Handle(_ context.Context, arguments json.RawMessage) (*mcp.ToolResult, error) {
	var args ReadArgs
	if err := json.Unmarshal(arguments, &args); err != nil {
		return toolError("Invalid arguments: %v", err), nil
	}
	if args.File == "" {
		return toolError("File path cannot be empty"), nil
	}

	absPath, err := validatePath(args.File)
	if err != nil {
		return toolError("%v", err), nil
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return toolError("Failed to read file: %v", err), nil
	}

	h.tracker.MarkRead(absPath)
	if h.lspManager != nil {
		go h.lspManager.TouchFile(context.Background(), absPath)
	}
	if h.tsIndex != nil {
		go h.tsIndex.UpdateFile(absPath)
	}

	lines := strings.Split(string(content), "\n")
	selectedContent, startLine, err := extractRange(lines, string(content), args.Start, args.End)
	if err != nil {
		return toolError("%v", err), nil
	}

	tagged := hashline.TagLines(selectedContent, startLine)
	taggedOutput := hashline.FormatTagged(tagged)

	rangeInfo := ""
	if args.Start > 0 || args.End > 0 {
		end := args.End
		if end <= 0 || end > len(lines) {
			end = len(lines)
		}
		rangeInfo = fmt.Sprintf(" (lines %d-%d)", startLine, end)
	}

	return &mcp.ToolResult{
		Content: []mcp.ContentBlock{{Type: "text", Text: fmt.Sprintf("Read %s%s (%d lines):\n\n%s", args.File, rangeInfo, len(tagged), taggedOutput)}},
	}, nil
}

// validatePath resolves a file path, ensuring it's within the working directory.
func validatePath(file string) (string, error) {
	absPath, err := filepath.Abs(file)
	if err != nil {
		return "", fmt.Errorf("invalid file path: %w", err)
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %w", err)
	}
	relPath, err := filepath.Rel(workingDir, absPath)
	if err != nil || strings.HasPrefix(relPath, "..") || filepath.IsAbs(relPath) {
		return "", fmt.Errorf("access denied: path outside working directory")
	}
	return absPath, nil
}

// extractRange returns the selected content and start line number for a line range.
func extractRange(lines []string, full string, start, end int) (string, int, error) {
	if start <= 0 && end <= 0 {
		return full, 1, nil
	}
	if start <= 0 {
		start = 1
	}
	if start < 1 || start > len(lines) {
		return "", 0, fmt.Errorf("start line %d out of range (file has %d lines)", start, len(lines))
	}
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}
	if start > end {
		return "", 0, fmt.Errorf("invalid range: start (%d) > end (%d)", start, end)
	}
	return strings.Join(lines[start-1:end], "\n"), start, nil
}
