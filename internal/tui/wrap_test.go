package tui

import (
	"strings"
	"testing"
)

func TestWrapANSIPreservesStyles(t *testing.T) {
	red := "\x1b[31m"
	bgBlack := "\x1b[40m"
	reset := "\x1b[0m"

	// A red-on-black string that will wrap across multiple lines.
	text := red + bgBlack + strings.Repeat("hello ", 20) + reset

	lines := wrapANSI(text, 30)

	if len(lines) < 2 {
		t.Fatalf("expected multiple lines, got %d", len(lines))
	}

	// Every continuation line must start with the active style sequences.
	for i, line := range lines {
		if i == 0 {
			// First line should start with the original sequences.
			if !strings.HasPrefix(line, red+bgBlack) {
				t.Errorf("line 0: expected style prefix, got %q", line[:min(len(line), 30)])
			}
			continue
		}
		// Continuation lines must have the style re-opened.
		if !strings.Contains(line, "\x1b[31m") {
			t.Errorf("line %d: missing red foreground sequence: %q", i, line)
		}
		if !strings.Contains(line, "\x1b[40m") {
			t.Errorf("line %d: missing black background sequence: %q", i, line)
		}
	}

	// All lines except the last should end with a reset.
	for i := 0; i < len(lines)-1; i++ {
		if !strings.HasSuffix(lines[i], "\x1b[m") {
			t.Errorf("line %d: should end with reset, got %q", i, lines[i][max(0, len(lines[i])-10):])
		}
	}
}

func TestWrapANSIResetClearsState(t *testing.T) {
	red := "\x1b[31m"
	reset := "\x1b[0m"

	// Style, some text, reset, more text — the continuation after reset
	// should NOT carry the red style.
	text := red + "aaaa" + reset + " " + strings.Repeat("b", 30)

	lines := wrapANSI(text, 10)

	if len(lines) < 2 {
		t.Fatalf("expected multiple lines, got %d", len(lines))
	}

	// After the reset, continuation lines should NOT have red.
	for i := 1; i < len(lines); i++ {
		if strings.Contains(lines[i], "\x1b[31m") {
			t.Errorf("line %d: should not have red after reset: %q", i, lines[i])
		}
	}
}

func TestWrapANSINoStylePassthrough(t *testing.T) {
	// Plain text should pass through unchanged (no spurious sequences added).
	text := "hello world this is a test of plain text wrapping"
	lines := wrapANSI(text, 15)

	for i, line := range lines {
		if strings.Contains(line, "\x1b") {
			t.Errorf("line %d: unexpected ANSI sequence in plain text: %q", i, line)
		}
	}
}
