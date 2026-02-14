package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xonecas/symb/internal/delta"
	"github.com/xonecas/symb/internal/hashline"
	"github.com/xonecas/symb/internal/lsp"
	"github.com/xonecas/symb/internal/mcp"
	"github.com/xonecas/symb/internal/treesitter"
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

// anchor is the JSON schema fragment for a hashline anchor object.
const anchorSchema = `{"type": "object", "properties": {"line": {"type": "integer", "description": "1-indexed line number"}, "hash": {"type": "string", "description": "2-char hex hash from Read output"}}, "required": ["line", "hash"]}`

// NewEditTool creates the Edit tool definition.
func NewEditTool() mcp.Tool {
	return mcp.Tool{
		Name: "Edit",
		Description: `Edit a file using hash-anchored operations. You MUST Read the file first to get line hashes.
Each line from Read is tagged as "linenum:hash|content". Use the line number and hash as anchors.
Exactly one operation per call: replace, insert, delete, or create.
If a hash does not match, the file changed since you read it — re-Read and retry.
After each edit you receive fresh hashes — use those for subsequent edits, not the old ones.`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file": {"type": "string", "description": "Path to the file to edit"},
				"replace": {
					"type": "object",
					"description": "Replace lines from start to end (inclusive) with new content",
					"properties": {
						"start":   ` + anchorSchema + `,
						"end":     ` + anchorSchema + `,
						"content": {"type": "string", "description": "Replacement text (may be multiple lines)"}
					},
					"required": ["start", "end", "content"]
				},
				"insert": {
					"type": "object",
					"description": "Insert new lines after the anchored line",
					"properties": {
						"after":   ` + anchorSchema + `,
						"content": {"type": "string", "description": "Text to insert (may be multiple lines)"}
					},
					"required": ["after", "content"]
				},
				"delete": {
					"type": "object",
					"description": "Delete lines from start to end (inclusive)",
					"properties": {
						"start": ` + anchorSchema + `,
						"end":   ` + anchorSchema + `
					},
					"required": ["start", "end"]
				},
				"create": {
					"type": "object",
					"description": "Create a new file (fails if file already exists)",
					"properties": {
						"content": {"type": "string", "description": "Full file content"}
					},
					"required": ["content"]
				}
			},
			"required": ["file"]
		}`),
	}
}

// EditHandler handles Edit tool calls.
type EditHandler struct {
	tracker      *FileReadTracker
	lspManager   *lsp.Manager
	tsIndex      *treesitter.Index
	deltaTracker *delta.Tracker
}

// NewEditHandler creates a handler for the Edit tool.
func NewEditHandler(tracker *FileReadTracker, lspManager *lsp.Manager, dt *delta.Tracker) *EditHandler {
	return &EditHandler{tracker: tracker, lspManager: lspManager, deltaTracker: dt}
}

// SetTSIndex sets the tree-sitter index for incremental updates on edit.
func (h *EditHandler) SetTSIndex(idx *treesitter.Index) { h.tsIndex = idx }

// Handle implements the mcp.ToolHandler interface.
func (h *EditHandler) Handle(ctx context.Context, arguments json.RawMessage) (*mcp.ToolResult, error) {
	var args EditArgs
	if err := json.Unmarshal(arguments, &args); err != nil {
		return toolError("Invalid arguments: %v", err), nil
	}
	if args.File == "" {
		return toolError("File path cannot be empty"), nil
	}
	if err := validateEditOps(args); err != nil {
		return toolError("%v", err), nil
	}

	absPath, err := validatePath(args.File)
	if err != nil {
		return toolError("%v", err), nil
	}

	if args.Create != nil {
		return h.handleCreate(ctx, absPath, args.File, args.Create)
	}

	if !h.tracker.WasRead(absPath) {
		return toolError("You must Read the file before editing it. Use Read on %s first — you need the line hashes.", args.File), nil
	}

	return h.applyEdit(ctx, absPath, args)
}

// validateEditOps ensures exactly one operation is specified.
func validateEditOps(args EditArgs) error {
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
		return fmt.Errorf("exactly one operation (replace, insert, delete, or create) must be specified")
	}
	return nil
}

// applyEdit reads the file, applies the edit operation, writes it back, and returns fresh hashes.
func (h *EditHandler) applyEdit(ctx context.Context, absPath string, args EditArgs) (*mcp.ToolResult, error) {
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

	if h.deltaTracker != nil {
		h.deltaTracker.RecordModify(absPath, content)
	}

	if err := os.WriteFile(absPath, []byte(result), 0600); err != nil {
		return toolError("Failed to write file: %v", err), nil
	}

	tagged := hashline.TagLines(result, 1)
	text := fmt.Sprintf("Edited %s (%d lines):\n\n%s", args.File, len(tagged), hashline.FormatTagged(tagged))

	if h.lspManager != nil {
		diags := h.lspManager.NotifyAndWait(ctx, absPath, 5*time.Second)
		text += lsp.FormatDiagnostics(args.File, diags)
	}
	if h.tsIndex != nil {
		h.tsIndex.UpdateFile(absPath)
	}

	return &mcp.ToolResult{
		Content: []mcp.ContentBlock{{Type: "text", Text: text}},
	}, nil
}

func (h *EditHandler) handleCreate(ctx context.Context, absPath, displayPath string, op *CreateOp) (*mcp.ToolResult, error) {
	// Fail if file already exists
	if _, err := os.Stat(absPath); err == nil {
		return toolError("File already exists: %s (use replace/insert/delete to modify)", displayPath), nil
	}

	// Create parent directories
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return toolError("Failed to create directories: %v", err), nil
	}

	if h.deltaTracker != nil {
		h.deltaTracker.RecordCreate(absPath)
	}

	if err := os.WriteFile(absPath, []byte(op.Content), 0600); err != nil {
		return toolError("Failed to create file: %v", err), nil
	}

	tagged := hashline.TagLines(op.Content, 1)
	taggedOutput := hashline.FormatTagged(tagged)

	text := fmt.Sprintf("Created %s (%d lines):\n\n%s", displayPath, len(tagged), taggedOutput)

	// Closed-loop LSP diagnostics for newly created file.
	if h.lspManager != nil {
		diags := h.lspManager.NotifyAndWait(ctx, absPath, 5*time.Second)
		text += lsp.FormatDiagnostics(displayPath, diags)
	}
	if h.tsIndex != nil {
		h.tsIndex.UpdateFile(absPath)
	}

	return &mcp.ToolResult{
		Content: []mcp.ContentBlock{{Type: "text", Text: text}},
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
