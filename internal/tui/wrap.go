package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// wrapANSI word-wraps an ANSI-styled string to the given width, returning
// the resulting visual lines. Styles are propagated across line breaks so
// each line is independently renderable.
func wrapANSI(s string, width int) []string {
	if width <= 0 || s == "" {
		return []string{s}
	}
	wrapped := ansi.Wordwrap(s, width, "")
	wrapped = ansi.Hardwrap(wrapped, width, true)
	lines := splitLines(wrapped)
	return propagateStyles(lines)
}

// propagateStyles ensures each line carries the ANSI style state from
// previous lines. After this, every line can be rendered independently
// with correct colors/attributes.
func propagateStyles(lines []string) []string {
	if len(lines) <= 1 {
		return lines
	}

	var activeSeqs []string // non-reset SGR sequences currently active

	for i, line := range lines {
		// Prepend active style to continuation lines.
		if i > 0 && len(activeSeqs) > 0 {
			lines[i] = strings.Join(activeSeqs, "") + line
		}

		// Scan this line for SGR sequences to update active state.
		activeSeqs = scanSGR(line, activeSeqs)

		// Append reset at end of each line (except last) so padding
		// after the line doesn't inherit the style.
		if i < len(lines)-1 && len(activeSeqs) > 0 {
			lines[i] = lines[i] + ansi.ResetStyle
		}
	}

	return lines
}

// scanSGR scans a line for SGR escape sequences and updates the active
// sequence list. Resets clear the list; other SGRs are appended.
func scanSGR(line string, active []string) []string {
	const esc = '\x1b'
	for j := 0; j < len(line); j++ {
		if line[j] != byte(esc) || j+1 >= len(line) || line[j+1] != '[' {
			continue
		}
		// Found ESC[, scan to find the 'm' terminator (SGR).
		k := j + 2
		for k < len(line) && line[k] != 'm' && line[k] != esc {
			k++
		}
		if k >= len(line) || line[k] != 'm' {
			continue
		}
		seq := line[j : k+1] // e.g. "\x1b[31m" or "\x1b[0m"
		params := line[j+2 : k]

		if isResetSGR(params) {
			active = active[:0]
		} else {
			active = append(active, seq)
		}
		j = k // skip past the sequence
	}
	return active
}

// isResetSGR returns true if the SGR parameter string represents a reset.
// A reset is "0", "" or standalone zero.
func isResetSGR(params string) bool {
	return params == "" || params == "0"
}

// splitLines splits on newline without the trailing empty element that
// strings.Split produces for a trailing newline.
func splitLines(s string) []string {
	lines := make([]string, 0, 8)
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	lines = append(lines, s[start:])
	return lines
}
