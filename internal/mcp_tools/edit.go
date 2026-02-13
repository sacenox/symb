package mcp_tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/xonecas/symb/internal/hashline"
	"github.com/xonecas/symb/internal/mcp"
)

// EditArgs represents arguments for the Edit tool.
// Exactly one of the operation fields (Replace, Insert, Delete) must be set.
type EditArgs struct {
	File    string     `json:"file"`
	Replace *ReplaceOp `json:"replace,omitempty"`
	Insert  *InsertOp  `json:"insert,omitempty"`
	Delete  *DeleteOp  `json:"delete,omitempty"`
	Create  *CreateOp  `json:"create,omitempty"`
}

// ReplaceOp replaces lines between start and end (inclusive) with new content.
type ReplaceOp struct {
	Start   hashline.Anchor `json:"start"`   // anchor for first line to replace
	End     hashline.Anchor `json:"end"`     // anchor for last line to replace
	Content string          `json:"content"` // replacement text (may be multiple lines)
}

// InsertOp inserts new lines after the anchored line.
type InsertOp struct {
	After   hashline.Anchor `json:"after"`   // anchor for the line to insert after
	Content string          `json:"content"` // text to insert (may be multiple lines)
}

// DeleteOp deletes lines between start and end (inclusive).
type DeleteOp struct {
	Start hashline.Anchor `json:"start"` // anchor for first line to delete
	End   hashline.Anchor `json:"end"`   // anchor for last line to delete
}

// CreateOp creates a new file with the given content.
type CreateOp struct {
	Content string `json:"content"` // full file content
}

// NewEditTool creates the Edit tool definition.
func NewEditTool() mcp.Tool {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file": map[string]interface{}{
				"type":        "string",
				"description": "Path to the file to edit",
			},
			"replace": map[string]interface{}{
				"type":        "object",
				"description": "Replace lines from start to end (inclusive) with new content",
				"properties": map[string]interface{}{
					"start": map[string]interface{}{
						"type":        "object",
						"description": "Anchor for first line to replace",
						"properties": map[string]interface{}{
							"line": map[string]interface{}{"type": "integer", "description": "1-indexed line number"},
							"hash": map[string]interface{}{"type": "string", "description": "2-char hex hash from Open output"},
						},
						"required": []string{"line", "hash"},
					},
					"end": map[string]interface{}{
						"type":        "object",
						"description": "Anchor for last line to replace",
						"properties": map[string]interface{}{
							"line": map[string]interface{}{"type": "integer", "description": "1-indexed line number"},
							"hash": map[string]interface{}{"type": "string", "description": "2-char hex hash from Open output"},
						},
						"required": []string{"line", "hash"},
					},
					"content": map[string]interface{}{"type": "string", "description": "Replacement text (may be multiple lines)"},
				},
				"required": []string{"start", "end", "content"},
			},
			"insert": map[string]interface{}{
				"type":        "object",
				"description": "Insert new lines after the anchored line",
				"properties": map[string]interface{}{
					"after": map[string]interface{}{
						"type":        "object",
						"description": "Anchor for the line to insert after",
						"properties": map[string]interface{}{
							"line": map[string]interface{}{"type": "integer", "description": "1-indexed line number"},
							"hash": map[string]interface{}{"type": "string", "description": "2-char hex hash from Open output"},
						},
						"required": []string{"line", "hash"},
					},
					"content": map[string]interface{}{"type": "string", "description": "Text to insert (may be multiple lines)"},
				},
				"required": []string{"after", "content"},
			},
			"delete": map[string]interface{}{
				"type":        "object",
				"description": "Delete lines from start to end (inclusive)",
				"properties": map[string]interface{}{
					"start": map[string]interface{}{
						"type":        "object",
						"description": "Anchor for first line to delete",
						"properties": map[string]interface{}{
							"line": map[string]interface{}{"type": "integer", "description": "1-indexed line number"},
							"hash": map[string]interface{}{"type": "string", "description": "2-char hex hash from Open output"},
						},
						"required": []string{"line", "hash"},
					},
					"end": map[string]interface{}{
						"type":        "object",
						"description": "Anchor for last line to delete",
						"properties": map[string]interface{}{
							"line": map[string]interface{}{"type": "integer", "description": "1-indexed line number"},
							"hash": map[string]interface{}{"type": "string", "description": "2-char hex hash from Open output"},
						},
						"required": []string{"line", "hash"},
					},
				},
				"required": []string{"start", "end"},
			},
			"create": map[string]interface{}{
				"type":        "object",
				"description": "Create a new file (fails if file already exists)",
				"properties": map[string]interface{}{
					"content": map[string]interface{}{"type": "string", "description": "Full file content"},
				},
				"required": []string{"content"},
			},
		},
		"required": []string{"file"},
	}

	schemaJSON, _ := json.Marshal(schema)

	return mcp.Tool{
		Name: "Edit",
		Description: `Edit a file using hash-anchored operations. You MUST Open the file first to get line hashes.
Each line from Open is tagged as "linenum:hash|content". Use the line number and hash as anchors.
Exactly one operation per call: replace, insert, delete, or create.
If a hash does not match, the file changed since you read it — re-Open and retry.
After each edit you receive fresh hashes — use those for subsequent edits, not the old ones.`,
		InputSchema: schemaJSON,
	}
}

// EditHandler handles Edit tool calls.
type EditHandler struct {
	program *tea.Program
	tracker *FileReadTracker
}

// NewEditHandler creates a handler for the Edit tool.
func NewEditHandler(tracker *FileReadTracker) *EditHandler {
	return &EditHandler{tracker: tracker}
}

// SetProgram sets the tea.Program instance after it's created.
func (h *EditHandler) SetProgram(program *tea.Program) {
	h.program = program
}

// Handle implements the mcp.ToolHandler interface.
func (h *EditHandler) Handle(ctx context.Context, arguments json.RawMessage) (*mcp.ToolResult, error) {
	var args EditArgs
	if err := json.Unmarshal(arguments, &args); err != nil {
		return toolError("Invalid arguments: %v", err), nil
	}

	if args.File == "" {
		return toolError("File path cannot be empty"), nil
	}

	// Count operations — exactly one must be set
	ops := 0
	if args.Replace != nil {
		ops++
	}
	if args.Insert != nil {
		ops++
	}
	if args.Delete != nil {
		ops++
	}
	if args.Create != nil {
		ops++
	}
	if ops != 1 {
		return toolError("Exactly one operation (replace, insert, delete, or create) must be specified"), nil
	}

	// Security: validate path
	absPath, err := filepath.Abs(args.File)
	if err != nil {
		return toolError("Invalid file path: %v", err), nil
	}

	workingDir, err := os.Getwd()
	if err != nil {
		return toolError("Failed to get working directory: %v", err), nil
	}

	relPath, err := filepath.Rel(workingDir, absPath)
	if err != nil || strings.HasPrefix(relPath, "..") || filepath.IsAbs(relPath) {
		return toolError("Access denied: path outside working directory"), nil
	}

	// Dispatch to operation handler
	if args.Create != nil {
		return h.handleCreate(absPath, args.File, args.Create)
	}

	// Enforce read-before-edit: file must have been opened first
	if !h.tracker.WasRead(absPath) {
		return toolError("You must Open the file before editing it. Use Open to read %s first — you need the line hashes.", args.File), nil
	}

	// All other ops require reading the existing file
	content, err := os.ReadFile(absPath)
	if err != nil {
		return toolError("Failed to read file: %v", err), nil
	}
	lines := strings.Split(string(content), "\n")

	var result string
	switch {
	case args.Replace != nil:
		result, err = applyReplace(lines, args.Replace)
	case args.Insert != nil:
		result, err = applyInsert(lines, args.Insert)
	case args.Delete != nil:
		result, err = applyDelete(lines, args.Delete)
	}

	if err != nil {
		return toolError("%v", err), nil
	}

	if err := os.WriteFile(absPath, []byte(result), 0600); err != nil {
		return toolError("Failed to write file: %v", err), nil
	}

	// Show updated file in TUI editor
	language := DetectLanguage(args.File)
	if h.program != nil {
		h.program.Send(OpenForUserMsg{
			Content:  result,
			Language: language,
			FilePath: args.File,
		})
	}

	// Return hashline-tagged view of the updated file
	tagged := hashline.TagLines(result, 1)
	taggedOutput := hashline.FormatTagged(tagged)

	return &mcp.ToolResult{
		Content: []mcp.ContentBlock{{Type: "text", Text: fmt.Sprintf("Edited %s (%d lines):\n\n%s", args.File, len(tagged), taggedOutput)}},
	}, nil
}

func (h *EditHandler) handleCreate(absPath, displayPath string, op *CreateOp) (*mcp.ToolResult, error) {
	// Fail if file already exists
	if _, err := os.Stat(absPath); err == nil {
		return toolError("File already exists: %s (use replace/insert/delete to modify)", displayPath), nil
	}

	// Create parent directories
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return toolError("Failed to create directories: %v", err), nil
	}

	if err := os.WriteFile(absPath, []byte(op.Content), 0600); err != nil {
		return toolError("Failed to create file: %v", err), nil
	}

	// Show in TUI
	language := DetectLanguage(displayPath)
	if h.program != nil {
		h.program.Send(OpenForUserMsg{
			Content:  op.Content,
			Language: language,
			FilePath: displayPath,
		})
	}

	tagged := hashline.TagLines(op.Content, 1)
	taggedOutput := hashline.FormatTagged(tagged)

	return &mcp.ToolResult{
		Content: []mcp.ContentBlock{{Type: "text", Text: fmt.Sprintf("Created %s (%d lines):\n\n%s", displayPath, len(tagged), taggedOutput)}},
	}, nil
}

func applyReplace(lines []string, op *ReplaceOp) (string, error) {
	if err := hashline.ValidateRange(op.Start, op.End, lines); err != nil {
		return "", fmt.Errorf("replace: %w", err)
	}

	newLines := make([]string, 0, len(lines))
	newLines = append(newLines, lines[:op.Start.Num-1]...)
	newLines = append(newLines, strings.Split(op.Content, "\n")...)
	newLines = append(newLines, lines[op.End.Num:]...)

	return strings.Join(newLines, "\n"), nil
}

func applyInsert(lines []string, op *InsertOp) (string, error) {
	if err := op.After.Validate(lines); err != nil {
		return "", fmt.Errorf("insert: after anchor: %w", err)
	}

	newLines := make([]string, 0, len(lines)+1)
	newLines = append(newLines, lines[:op.After.Num]...)
	newLines = append(newLines, strings.Split(op.Content, "\n")...)
	newLines = append(newLines, lines[op.After.Num:]...)

	return strings.Join(newLines, "\n"), nil
}

func applyDelete(lines []string, op *DeleteOp) (string, error) {
	if err := hashline.ValidateRange(op.Start, op.End, lines); err != nil {
		return "", fmt.Errorf("delete: %w", err)
	}

	newLines := make([]string, 0, len(lines))
	newLines = append(newLines, lines[:op.Start.Num-1]...)
	newLines = append(newLines, lines[op.End.Num:]...)

	return strings.Join(newLines, "\n"), nil
}
