package mcptools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xonecas/symb/internal/delta"
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
Commands run inside the project working directory. Shell state (cwd, env vars) persists across calls within the same session.
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
	sh           *shell.Shell
	deltaTracker *delta.Tracker
	// OnOutput is called with incremental output chunks for real-time streaming.
	// May be nil.
	OnOutput func(chunk string)
}

// NewShellHandler creates a handler for the Shell tool.
func NewShellHandler(sh *shell.Shell, dt *delta.Tracker) *ShellHandler {
	return &ShellHandler{sh: sh, deltaTracker: dt}
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

	// Snapshot the shell's working directory for undo — only when the delta
	// tracker is active (has a turn), to avoid expensive walks on read-only commands.
	shellCwd := h.sh.Dir()
	trackDeltas := h.deltaTracker != nil && h.deltaTracker.TurnID() > 0
	var preSnap map[string]fileSnapshot
	if trackDeltas {
		preSnap = snapshotDir(shellCwd)
	}

	// Execute command with streaming output.
	var stdout, stderr bytes.Buffer

	var execErr error
	if h.OnOutput != nil {
		sw := &streamWriter{buf: &stdout, onChunk: h.OnOutput}
		execErr = h.sh.ExecStream(ctx, args.Command, sw, &stderr)
	} else {
		execErr = h.sh.ExecStream(ctx, args.Command, &stdout, &stderr)
	}

	// Post-execution: diff the *original* cwd to record deltas for undo.
	// Always use shellCwd (the pre-exec directory) for both snapshots so a
	// `cd` inside the command doesn't cause a cross-directory mismatch.
	if trackDeltas {
		postSnap := snapshotDir(shellCwd)
		recordDeltas(h.deltaTracker, shellCwd, preSnap, postSnap)
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

const maxOutputChars = 30000
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

// ---------------------------------------------------------------------------
// CWD snapshot for undo
// ---------------------------------------------------------------------------

// fileSnapshot holds mtime+size+content for change detection and undo.
type fileSnapshot struct {
	modTime time.Time
	size    int64
	content []byte // pre-read for undo; nil for large files
}

// maxSnapshotFileSize is the max file size we pre-read for undo (1 MB).
const maxSnapshotFileSize = 1 << 20

// skipDirs are directories skipped during snapshot walks.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "__pycache__": true,
	".venv": true, "vendor": true, ".cache": true, ".next": true,
	"dist": true, "build": true, "target": true,
}

// snapshotDir walks root and returns a map of relative path -> fileSnapshot.
// Files under maxSnapshotFileSize have their content pre-read for undo.
func snapshotDir(root string) map[string]fileSnapshot {
	snap := make(map[string]fileSnapshot)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		fs := fileSnapshot{modTime: info.ModTime(), size: info.Size()}
		if info.Size() <= maxSnapshotFileSize {
			fs.content, _ = os.ReadFile(path)
		}
		snap[rel] = fs
		return nil
	})
	return snap
}

// recordDeltas compares pre/post snapshots and records deltas for undo.
func recordDeltas(dt *delta.Tracker, root string, pre, post map[string]fileSnapshot) {
	// New or modified files.
	for rel, postInfo := range post {
		absPath := filepath.Join(root, rel)
		preInfo, existed := pre[rel]
		if !existed {
			dt.RecordCreate(absPath)
			continue
		}
		if preInfo.modTime != postInfo.modTime || preInfo.size != postInfo.size {
			// File was modified — use pre-read content for undo.
			dt.RecordModify(absPath, preInfo.content)
		}
	}
	// Deleted files — existed in pre but not in post.
	for rel, preInfo := range pre {
		if _, exists := post[rel]; !exists {
			absPath := filepath.Join(root, rel)
			if preInfo.content != nil {
				// We have the original content; record as modify so undo restores it.
				dt.RecordModify(absPath, preInfo.content)
			}
			// If content is nil (file was > maxSnapshotFileSize), we can't
			// restore it. Skip rather than writing a 0-byte file on undo.
		}
	}
}
