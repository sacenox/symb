package tui

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"

	"github.com/xonecas/symb/internal/tui/editor"
)

// GitFileMarkers runs `git diff` for the given file path and returns
// gutter markers keyed by 0-indexed line number.
// Returns nil (not error) when outside a git repo or on a new/untracked file.
func GitFileMarkers(ctx context.Context, filePath string) map[int]editor.GutterMark {
	cmd := exec.CommandContext(ctx, "git", "diff", "--unified=0", "--", filePath)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	// Ignore errors — not-a-repo or untracked files just return nil.
	_ = cmd.Run()

	return ParseDiffMarkers(stdout.String())
}

// ParseDiffMarkers parses unified diff output (--unified=0) and returns
// gutter markers keyed by 0-indexed line number in the new file.
func ParseDiffMarkers(diff string) map[int]editor.GutterMark {
	if strings.TrimSpace(diff) == "" {
		return nil
	}

	markers := make(map[int]editor.GutterMark)

	for _, line := range strings.Split(diff, "\n") {
		if !strings.HasPrefix(line, "@@ ") {
			continue
		}

		// Parse hunk header: @@ -oldStart[,oldCount] +newStart[,newCount] @@
		newStart, newCount, oldCount, ok := parseHunkHeader(line)
		if !ok {
			continue
		}

		if newCount == 0 {
			// Pure deletion: no new lines, mark the line before as delete.
			// newStart points to the line after the deletion.
			row := newStart - 1 // convert to 0-indexed
			if row < 0 {
				row = 0
			}
			markers[row] = editor.GutterDelete
		} else if oldCount == 0 {
			// Pure addition.
			for i := 0; i < newCount; i++ {
				markers[newStart-1+i] = editor.GutterAdd
			}
		} else {
			// Modification (some old lines replaced by some new lines).
			for i := 0; i < newCount; i++ {
				markers[newStart-1+i] = editor.GutterChange
			}
		}
	}

	if len(markers) == 0 {
		return nil
	}
	return markers
}

// parseHunkHeader extracts newStart, newCount, oldCount from a @@ line.
// Format: @@ -oldStart[,oldCount] +newStart[,newCount] @@
func parseHunkHeader(line string) (newStart, newCount, oldCount int, ok bool) {
	// Strip leading "@@ " and trailing " @@..."
	idx := strings.Index(line[3:], " @@")
	if idx < 0 {
		return 0, 0, 0, false
	}
	header := line[3 : 3+idx] // e.g. "-10,3 +12,5"

	parts := strings.Fields(header)
	if len(parts) != 2 {
		return 0, 0, 0, false
	}

	// Parse old range.
	old := strings.TrimPrefix(parts[0], "-")
	_, oldCount = parseRange(old)

	// Parse new range.
	neu := strings.TrimPrefix(parts[1], "+")
	newStart, newCount = parseRange(neu)

	if newStart == 0 {
		return 0, 0, 0, false
	}
	return newStart, newCount, oldCount, true
}

// parseRange parses "start,count" or "start" (count defaults to 1).
func parseRange(s string) (start, count int) {
	if idx := strings.IndexByte(s, ','); idx >= 0 {
		start, _ = strconv.Atoi(s[:idx])
		count, _ = strconv.Atoi(s[idx+1:])
		return start, count
	}
	start, _ = strconv.Atoi(s)
	return start, 1
}
