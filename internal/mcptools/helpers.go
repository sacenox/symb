package mcptools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xonecas/symb/internal/mcp"
)

// validatePath resolves a file path, ensuring it's within the working directory.
func validatePath(file string) (string, error) {
	workingDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %w", err)
	}
	return validatePathWithRoot(file, workingDir)
}

func validatePathWithRoot(file, root string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("invalid root path: %w", err)
	}
	var absPath string
	if filepath.IsAbs(file) {
		absPath = file
	} else {
		absPath = filepath.Join(rootAbs, file)
	}
	absPath, err = filepath.Abs(absPath)
	if err != nil {
		return "", fmt.Errorf("invalid file path: %w", err)
	}
	relPath, err := filepath.Rel(rootAbs, absPath)
	if err != nil || strings.HasPrefix(relPath, "..") || filepath.IsAbs(relPath) {
		return "", fmt.Errorf("access denied: path outside working directory")
	}
	return absPath, nil
}

// toolError returns an error ToolResult.
func toolError(format string, args ...interface{}) *mcp.ToolResult {
	return &mcp.ToolResult{
		Content: []mcp.ContentBlock{{Type: "text", Text: fmt.Sprintf(format, args...)}},
		IsError: true,
	}
}

// toolText returns a text ToolResult.
func toolText(text string) *mcp.ToolResult {
	return &mcp.ToolResult{
		Content: []mcp.ContentBlock{{Type: "text", Text: text}},
		IsError: false,
	}
}
