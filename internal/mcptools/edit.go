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

const (
	// windowThreshold is the minimum line count to trigger windowed output.
	windowThreshold = 50
	// windowContext is the number of lines shown above/below the edit region.
	windowContext = 20
)

// editRegion describes which lines (1-indexed, inclusive) were affected in the new file.
type editRegion struct {
	start int // first affected line
	end   int // last affected line
}

// EditArgs represents arguments for the Edit tool (flat schema).
type EditArgs struct {
	File      string `json:"file"`
	Operation string `json:"operation"`
	Start     string `json:"start,omitempty"`   // "line:hash" anchor
	End       string `json:"end,omitempty"`     // "line:hash" anchor
	After     string `json:"after,omitempty"`   // "line:hash" anchor (insert)
	Content   string `json:"content,omitempty"` // text content
}

// NewEditTool creates the Edit tool definition.
func NewEditTool() mcp.Tool {
	return mcp.Tool{
		Name: "Edit",
		Description: `Edit a file using hash-anchored operations. You MUST Read the file first to get line hashes.
Each line from Read is tagged as "linenum:hash|content". Use "line:hash" strings as anchors.
Exactly one operation per call: replace, insert, delete, or create.
If a hash does not match, the file changed since you read it — re-Read and retry.
After each edit you receive fresh hashes — use those for subsequent edits, not the old ones.
Never use Shell to write files — always use Edit.

Operations:
- replace: replace lines from start anchor to end anchor with content
- insert: insert content after the 'after' anchor line
- delete: delete lines from start anchor to end anchor
- create: create a new file with content (fails if file exists; no anchors needed)`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file":      {"type": "string", "description": "Path to the file to edit"},
				"operation": {"type": "string", "enum": ["replace", "insert", "delete", "create"], "description": "The edit operation to perform"},
				"start":     {"type": "string", "description": "Start anchor as 'line:hash' (replace, delete)"},
				"end":       {"type": "string", "description": "End anchor as 'line:hash' (replace, delete)"},
				"after":     {"type": "string", "description": "Insert-after anchor as 'line:hash' (insert)"},
				"content":   {"type": "string", "description": "Text content (replace, insert, create)"}
			},
			"required": ["file", "operation"]
		}`),
	}
}

// EditHandler handles Edit tool calls.
type EditHandler struct {
	tracker      *FileReadTracker
	lspManager   *lsp.Manager
	tsIndex      *treesitter.Index
	deltaTracker *delta.Tracker
	rootDir      string
}

// NewEditHandler creates a handler for the Edit tool.
func NewEditHandler(tracker *FileReadTracker, lspManager *lsp.Manager, dt *delta.Tracker) *EditHandler {
	return &EditHandler{tracker: tracker, lspManager: lspManager, deltaTracker: dt}
}

// SetTSIndex sets the tree-sitter index for incremental updates on edit.
func (h *EditHandler) SetTSIndex(idx *treesitter.Index) { h.tsIndex = idx }

// SetRootDir overrides the base directory for path validation.
func (h *EditHandler) SetRootDir(root string) { h.rootDir = root }

// Handle implements the mcp.ToolHandler interface.
func (h *EditHandler) Handle(ctx context.Context, arguments json.RawMessage) (*mcp.ToolResult, error) {
	var args EditArgs
	if err := json.Unmarshal(arguments, &args); err != nil {
		return toolError("Invalid arguments: %v", err), nil
	}
	if args.File == "" {
		return toolError("file is required"), nil
	}
	if args.Operation == "" {
		return toolError("operation is required (replace, insert, delete, or create)"), nil
	}

	var absPath string
	var err error
	if h.rootDir != "" {
		absPath, err = validatePathWithRoot(args.File, h.rootDir)
	} else {
		absPath, err = validatePath(args.File)
	}
	if err != nil {
		return toolError("%v", err), nil
	}

	if args.Operation == "create" {
		return h.handleCreate(ctx, absPath, args.File, args.Content)
	}

	if !h.tracker.WasRead(absPath) {
		return toolError("You must Read the file before editing it. Use Read on %s first — you need the line hashes.", args.File), nil
	}

	return h.applyEdit(ctx, absPath, args)
}

// applyEdit reads the file, applies the edit operation, writes it back, and returns fresh hashes.
func (h *EditHandler) applyEdit(ctx context.Context, absPath string, args EditArgs) (*mcp.ToolResult, error) {
	content, err := os.ReadFile(absPath)
	if err != nil {
		return toolError("Failed to read file: %v", err), nil
	}
	lines := strings.Split(string(content), "\n")

	var result string
	var region editRegion
	switch args.Operation {
	case "replace":
		result, region, err = applyReplace(lines, args)
	case "insert":
		result, region, err = applyInsert(lines, args)
	case "delete":
		result, region, err = applyDelete(lines, args)
	default:
		return toolError("unknown operation %q: use replace, insert, delete, or create", args.Operation), nil
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
	text := formatEditResponse(args.File, tagged, region)

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

func (h *EditHandler) handleCreate(ctx context.Context, absPath, displayPath, content string) (*mcp.ToolResult, error) {
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

	if err := os.WriteFile(absPath, []byte(content), 0600); err != nil {
		return toolError("Failed to create file: %v", err), nil
	}

	tagged := hashline.TagLines(content, 1)
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

// formatEditResponse builds the response text, using windowed output for large files.
func formatEditResponse(displayPath string, tagged []hashline.TaggedLine, region editRegion) string {
	total := len(tagged)
	if total <= windowThreshold {
		return fmt.Sprintf("Edited %s (%d lines):\n\n%s", displayPath, total, hashline.FormatTagged(tagged))
	}

	// Clamp window bounds.
	winStart := region.start - windowContext
	if winStart < 1 {
		winStart = 1
	}
	winEnd := region.end + windowContext
	if winEnd > total {
		winEnd = total
	}

	window := tagged[winStart-1 : winEnd] // tagged is 0-indexed, line nums are 1-indexed
	return fmt.Sprintf("Edited %s (%d lines, showing %d–%d):\n\n%s",
		displayPath, total, winStart, winEnd, hashline.FormatTagged(window))
}

func applyReplace(lines []string, args EditArgs) (string, editRegion, error) {
	start, err := hashline.ParseAnchor(args.Start)
	if err != nil {
		return "", editRegion{}, fmt.Errorf("replace start: %w", err)
	}
	end, err := hashline.ParseAnchor(args.End)
	if err != nil {
		return "", editRegion{}, fmt.Errorf("replace end: %w", err)
	}
	if err := hashline.ValidateRange(lines, &start, &end); err != nil {
		return "", editRegion{}, fmt.Errorf("replace: %w", err)
	}

	inserted := strings.Split(args.Content, "\n")
	newLines := make([]string, 0, len(lines))
	newLines = append(newLines, lines[:start.Num-1]...)
	newLines = append(newLines, inserted...)
	newLines = append(newLines, lines[end.Num:]...)

	region := editRegion{
		start: start.Num,
		end:   start.Num + len(inserted) - 1,
	}
	return strings.Join(newLines, "\n"), region, nil
}

func applyInsert(lines []string, args EditArgs) (string, editRegion, error) {
	after, err := hashline.ParseAnchor(args.After)
	if err != nil {
		return "", editRegion{}, fmt.Errorf("insert after: %w", err)
	}
	if err := after.Validate(lines); err != nil {
		return "", editRegion{}, fmt.Errorf("insert: after anchor: %w", err)
	}

	inserted := strings.Split(args.Content, "\n")
	newLines := make([]string, 0, len(lines)+len(inserted))
	newLines = append(newLines, lines[:after.Num]...)
	newLines = append(newLines, inserted...)
	newLines = append(newLines, lines[after.Num:]...)

	region := editRegion{
		start: after.Num + 1,
		end:   after.Num + len(inserted),
	}
	return strings.Join(newLines, "\n"), region, nil
}

func applyDelete(lines []string, args EditArgs) (string, editRegion, error) {
	start, err := hashline.ParseAnchor(args.Start)
	if err != nil {
		return "", editRegion{}, fmt.Errorf("delete start: %w", err)
	}
	end, err := hashline.ParseAnchor(args.End)
	if err != nil {
		return "", editRegion{}, fmt.Errorf("delete end: %w", err)
	}
	if err := hashline.ValidateRange(lines, &start, &end); err != nil {
		return "", editRegion{}, fmt.Errorf("delete: %w", err)
	}

	newLines := make([]string, 0, len(lines))
	newLines = append(newLines, lines[:start.Num-1]...)
	newLines = append(newLines, lines[end.Num:]...)

	// The "region" is where the deletion happened — show surrounding context.
	regionLine := start.Num - 1
	if regionLine < 1 {
		regionLine = 1
	}
	region := editRegion{
		start: regionLine,
		end:   regionLine,
	}
	return strings.Join(newLines, "\n"), region, nil
}
