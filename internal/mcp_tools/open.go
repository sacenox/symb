package mcp_tools

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
)

// ReadArgs represents arguments for the Read tool.
type ReadArgs struct {
	File  string `json:"file"`
	Start int    `json:"start,omitempty"` // Optional: start line (1-indexed)
	End   int    `json:"end,omitempty"`   // Optional: end line (1-indexed)
}

// ShowMsg is the message sent to TUI to display content in the editor pane.
type ShowMsg struct {
	Content  string
	Language string
	FilePath string // display path (may be relative); empty for non-file content
	AbsPath  string // absolute path for matching LSP diagnostics; empty for non-file content
}

// NewReadTool creates the Read tool definition.
func NewReadTool() mcp.Tool {
	return mcp.Tool{
		Name:        "Read",
		Description: `Reads a file and returns hashline-tagged content. Each line is returned as "linenum:hash|content". You MUST Read a file before editing it with Edit. Use start/end for line ranges. Does NOT display in the editor — use Show for that.`,
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
}

// NewReadHandler creates a handler for the Read tool.
func NewReadHandler(tracker *FileReadTracker, lspManager *lsp.Manager) *ReadHandler {
	return &ReadHandler{tracker: tracker, lspManager: lspManager}
}

// Handle implements the mcp.ToolHandler interface.
func (h *ReadHandler) Handle(ctx context.Context, arguments json.RawMessage) (*mcp.ToolResult, error) {
	var args ReadArgs
	if err := json.Unmarshal(arguments, &args); err != nil {
		return &mcp.ToolResult{
			Content: []mcp.ContentBlock{{Type: "text", Text: fmt.Sprintf("Invalid arguments: %v", err)}},
			IsError: true,
		}, nil
	}

	if args.File == "" {
		return &mcp.ToolResult{
			Content: []mcp.ContentBlock{{Type: "text", Text: "File path cannot be empty"}},
			IsError: true,
		}, nil
	}

	// Security: Convert to absolute path and validate
	absPath, err := filepath.Abs(args.File)
	if err != nil {
		return &mcp.ToolResult{
			Content: []mcp.ContentBlock{{Type: "text", Text: fmt.Sprintf("Invalid file path: %v", err)}},
			IsError: true,
		}, nil
	}

	// Get current working directory for validation
	workingDir, err := os.Getwd()
	if err != nil {
		return &mcp.ToolResult{
			Content: []mcp.ContentBlock{{Type: "text", Text: fmt.Sprintf("Failed to get working directory: %v", err)}},
			IsError: true,
		}, nil
	}

	// Security: Prevent path traversal - only allow files within or below working directory
	relPath, err := filepath.Rel(workingDir, absPath)
	if err != nil || strings.HasPrefix(relPath, "..") || filepath.IsAbs(relPath) {
		return &mcp.ToolResult{
			Content: []mcp.ContentBlock{{Type: "text", Text: "Access denied: path outside working directory"}},
			IsError: true,
		}, nil
	}

	// Read file content
	content, err := os.ReadFile(absPath)
	if err != nil {
		return &mcp.ToolResult{
			Content: []mcp.ContentBlock{{Type: "text", Text: fmt.Sprintf("Failed to read file: %v", err)}},
			IsError: true,
		}, nil
	}

	// Record that this file was read (enables editing)
	h.tracker.MarkRead(absPath)

	// Warm up LSP for this file (fire-and-forget).
	// Use Background context since this outlives the tool-call context.
	if h.lspManager != nil {
		go h.lspManager.TouchFile(context.Background(), absPath)
	}

	lines := strings.Split(string(content), "\n")

	// Extract line range if specified
	var selectedContent string
	if args.Start > 0 || args.End > 0 {
		start := args.Start
		end := args.End

		// Default start to 1 if not specified
		if start <= 0 {
			start = 1
		}

		// Validate start is in range
		if start < 1 || start > len(lines) {
			return &mcp.ToolResult{
				Content: []mcp.ContentBlock{{Type: "text", Text: fmt.Sprintf("Start line %d out of range (file has %d lines)", start, len(lines))}},
				IsError: true,
			}, nil
		}

		// Default end to end of file if not specified or out of range
		if end <= 0 || end > len(lines) {
			end = len(lines)
		}

		// Validate range
		if start > end {
			return &mcp.ToolResult{
				Content: []mcp.ContentBlock{{Type: "text", Text: fmt.Sprintf("Invalid range: start (%d) > end (%d)", start, end)}},
				IsError: true,
			}, nil
		}

		// Extract range (convert to 0-indexed)
		selectedContent = strings.Join(lines[start-1:end], "\n")
	} else {
		selectedContent = string(content)
	}

	// Return hashline-tagged content to the LLM
	startLine := 1
	if args.Start > 0 {
		startLine = args.Start
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
		IsError: false,
	}, nil
}

// DetectLanguage returns the Chroma language identifier based on file extension.
func DetectLanguage(path string) string {
	// Map common extensions to Chroma language identifiers
	languageMap := map[string]string{
		".go":         "go",
		".py":         "python",
		".js":         "javascript",
		".ts":         "typescript",
		".jsx":        "jsx",
		".tsx":        "tsx",
		".java":       "java",
		".c":          "c",
		".cpp":        "cpp",
		".cc":         "cpp",
		".h":          "c",
		".hpp":        "cpp",
		".cs":         "csharp",
		".rb":         "ruby",
		".php":        "php",
		".rs":         "rust",
		".swift":      "swift",
		".kt":         "kotlin",
		".scala":      "scala",
		".sh":         "bash",
		".bash":       "bash",
		".zsh":        "zsh",
		".fish":       "fish",
		".ps1":        "powershell",
		".r":          "r",
		".sql":        "sql",
		".html":       "html",
		".htm":        "html",
		".xml":        "xml",
		".css":        "css",
		".scss":       "scss",
		".sass":       "sass",
		".less":       "less",
		".json":       "json",
		".yaml":       "yaml",
		".yml":        "yaml",
		".toml":       "toml",
		".ini":        "ini",
		".conf":       "nginx",
		".md":         "markdown",
		".markdown":   "markdown",
		".tex":        "tex",
		".vim":        "vim",
		".lua":        "lua",
		".perl":       "perl",
		".pl":         "perl",
		".dockerfile": "docker",
		".proto":      "protobuf",
	}

	ext := strings.ToLower(filepath.Ext(path))
	if lang, ok := languageMap[ext]; ok {
		return lang
	}

	// Check for specific filenames
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "dockerfile":
		return "docker"
	case "makefile":
		return "make"
	case "gemfile":
		return "ruby"
	case "rakefile":
		return "ruby"
	}

	return "text" // Default fallback
}
