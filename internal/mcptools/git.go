package mcptools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/xonecas/symb/internal/mcp"
)

// GitStatusArgs represents arguments for the GitStatus tool.
type GitStatusArgs struct {
	Long bool `json:"long,omitempty"` // Use long format (default: false, short format)
}

// GitDiffArgs represents arguments for the GitDiff tool.
type GitDiffArgs struct {
	File   string `json:"file,omitempty"`   // Optional: specific file to diff
	Staged bool   `json:"staged,omitempty"` // Diff staged changes instead of unstaged
}

// NewGitStatusTool creates the GitStatus tool definition.
func NewGitStatusTool() mcp.Tool {
	return mcp.Tool{
		Name:        "GitStatus",
		Description: "Show the working tree status. Returns modified, staged, and untracked files.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"long": {"type": "boolean", "description": "Use long format output. Default: false (short format)"}
			}
		}`),
	}
}

// NewGitDiffTool creates the GitDiff tool definition.
func NewGitDiffTool() mcp.Tool {
	return mcp.Tool{
		Name:        "GitDiff",
		Description: "Show changes between working tree and index (unstaged), or between index and HEAD (staged). Returns unified diff output.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file":   {"type": "string", "description": "Optional: specific file path to diff. If omitted, diffs all changed files."},
				"staged": {"type": "boolean", "description": "If true, show staged (cached) changes. Default: false (unstaged changes)"}
			}
		}`),
	}
}

// runGit executes a git command and returns stdout, or a ToolResult error.
func runGit(ctx context.Context, args ...string) (string, *mcp.ToolResult) {
	cmd := exec.CommandContext(ctx, "git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// git diff returns exit code 1 when there are differences — that's not an error.
		if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 1 && stderr.Len() == 0 {
			return stdout.String(), nil
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", &mcp.ToolResult{
			Content: []mcp.ContentBlock{{Type: "text", Text: fmt.Sprintf("git error: %s", msg)}},
			IsError: true,
		}
	}
	return stdout.String(), nil
}

// MakeGitStatusHandler creates a handler for the GitStatus tool.
func MakeGitStatusHandler() mcp.ToolHandler {
	return func(ctx context.Context, arguments json.RawMessage) (*mcp.ToolResult, error) {
		var args GitStatusArgs
		if len(arguments) > 0 {
			if err := json.Unmarshal(arguments, &args); err != nil {
				return &mcp.ToolResult{
					Content: []mcp.ContentBlock{{Type: "text", Text: fmt.Sprintf("Invalid arguments: %v", err)}},
					IsError: true,
				}, nil
			}
		}

		gitArgs := []string{"status"}
		if !args.Long {
			gitArgs = append(gitArgs, "--short")
		}

		out, errResult := runGit(ctx, gitArgs...)
		if errResult != nil {
			return errResult, nil
		}

		if strings.TrimSpace(out) == "" {
			out = "nothing to commit, working tree clean"
		}

		return &mcp.ToolResult{
			Content: []mcp.ContentBlock{{Type: "text", Text: out}},
		}, nil
	}
}

// MakeGitDiffHandler creates a handler for the GitDiff tool.
func MakeGitDiffHandler() mcp.ToolHandler {
	return func(ctx context.Context, arguments json.RawMessage) (*mcp.ToolResult, error) {
		var args GitDiffArgs
		if len(arguments) > 0 {
			if err := json.Unmarshal(arguments, &args); err != nil {
				return &mcp.ToolResult{
					Content: []mcp.ContentBlock{{Type: "text", Text: fmt.Sprintf("Invalid arguments: %v", err)}},
					IsError: true,
				}, nil
			}
		}

		gitArgs := []string{"diff"}
		if args.Staged {
			gitArgs = append(gitArgs, "--cached")
		}
		if args.File != "" {
			gitArgs = append(gitArgs, "--", args.File)
		}

		out, errResult := runGit(ctx, gitArgs...)
		if errResult != nil {
			return errResult, nil
		}

		if strings.TrimSpace(out) == "" {
			label := "unstaged"
			if args.Staged {
				label = "staged"
			}
			out = fmt.Sprintf("no %s changes", label)
		}

		return &mcp.ToolResult{
			Content: []mcp.ContentBlock{{Type: "text", Text: out}},
		}, nil
	}
}
