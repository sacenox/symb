package mcptools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/xonecas/symb/internal/mcp"
	"github.com/xonecas/symb/internal/shell"
)

// ShellArgs are the arguments to the Shell tool.
type ShellArgs struct {
	Command     string `json:"command"`
	Description string `json:"description"`
	Timeout     int    `json:"timeout,omitempty"` // seconds, default 60
}

// NewShellTool creates the Shell tool definition.
func NewShellTool() mcp.Tool {
	return mcp.Tool{
		Name: "Shell",
		Description: `Execute a shell command in an in-process POSIX interpreter.
Commands run inside the project working directory. The shell is anchored to the project root — you cannot cd outside it. Env vars persist across calls.
Dangerous commands (network, sudo, package managers, system modification) are blocked.
Use this for: running builds, tests, linters, git operations, file manipulation, and inspecting project state.`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"command":     {"type": "string", "description": "The shell command to execute"},
				"description": {"type": "string", "description": "Brief description of what this command does (5-10 words)"},
				"timeout":     {"type": "integer", "description": "Timeout in seconds (default 60)"}
			},
			"required": ["command", "description"]
		}`),
	}
}

// ShellHandler handles Shell tool calls.
type ShellHandler struct {
	sh *shell.Shell
	// OnOutput is called with incremental output chunks for real-time streaming.
	// May be nil.
	OnOutput func(chunk string)
}

// NewShellHandler creates a handler for the Shell tool.
func NewShellHandler(sh *shell.Shell) *ShellHandler {
	return &ShellHandler{sh: sh}
}

// Handle implements the mcp.ToolHandler interface.
func (h *ShellHandler) Handle(ctx context.Context, arguments json.RawMessage) (*mcp.ToolResult, error) {
	var args ShellArgs
	if err := json.Unmarshal(arguments, &args); err != nil {
		return toolError("Invalid arguments: %v", err), nil
	}
	if args.Command == "" {
		return toolError("command is required"), nil
	}

	timeout := 60
	if args.Timeout > 0 {
		timeout = args.Timeout
	}
	if timeout > maxTimeoutSec {
		timeout = maxTimeoutSec
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	// Execute command with streaming output.
	var stdout, stderr bytes.Buffer

	var execErr error
	if h.OnOutput != nil {
		sw := &streamWriter{buf: &stdout, onChunk: h.OnOutput}
		execErr = h.sh.ExecStream(ctx, args.Command, sw, &stderr)
	} else {
		execErr = h.sh.ExecStream(ctx, args.Command, &stdout, &stderr)
	}

	// Format result.
	exitCode := shell.ExitCode(execErr)
	output := formatShellOutput(stdout.String(), stderr.String(), exitCode, ctx.Err())

	// Ensure non-empty output — some providers reject empty tool results.
	if output == "" {
		output = "(no output)\n"
	}

	if len([]rune(output)) > maxOutputChars {
		output = truncateMiddle(output, maxOutputChars)
	}

	if exitCode != 0 {
		return &mcp.ToolResult{
			Content: []mcp.ContentBlock{{Type: "text", Text: output}},
			IsError: true,
		}, nil
	}
	return toolText(output), nil
}

const maxOutputChars = 15000
const maxTimeoutSec = 600 // 10 minutes

// streamWriter wraps a bytes.Buffer and calls onChunk for each Write.
type streamWriter struct {
	buf     *bytes.Buffer
	onChunk func(string)
}

func (w *streamWriter) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	if n > 0 && w.onChunk != nil {
		w.onChunk(string(p[:n]))
	}
	return n, err
}

func formatShellOutput(stdout, stderr string, exitCode int, ctxErr error) string {
	var b strings.Builder
	if stdout != "" {
		b.WriteString(stdout)
		if !strings.HasSuffix(stdout, "\n") {
			b.WriteByte('\n')
		}
	}
	if stderr != "" {
		b.WriteString(stderr)
		if !strings.HasSuffix(stderr, "\n") {
			b.WriteByte('\n')
		}
	}
	if ctxErr != nil {
		fmt.Fprintf(&b, "[timed out]\n")
	}
	if exitCode != 0 {
		fmt.Fprintf(&b, "[exit code: %d]\n", exitCode)
	}
	return b.String()
}

func truncateMiddle(s string, maxChars int) string {
	runes := []rune(s)
	if len(runes) <= maxChars {
		return s
	}
	half := maxChars / 2
	return string(runes[:half]) + "\n\n... [truncated] ...\n\n" + string(runes[len(runes)-half:])
}
