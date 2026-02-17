package editor

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestLineWidthWithTabs(t *testing.T) {
	ed := New()
	ed.ShowLineNumbers = true
	ed.ReadOnly = true
	ed.Language = "go"
	ed.SyntaxTheme = "github-dark"
	ed.BgColor = lipgloss.Color("#000000")
	ed.LineNumStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#1c1c1c"))
	ed.CursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#00AA00"))

	content := "\t\tcreds, err := config.LoadCredentials()\n\t\tfmt.Printf(\"Error loading credentials: %v\\n\", err)\n\t\t\tapiKey := creds.GetAPIKey(providerCfg.APIKeyName)\n\t\t\tfactory = provider.NewOpenCodeFactory(name, providerCfg.Model, apiKey)"

	ed.SetWidth(50)
	ed.SetHeight(6)
	ed.SetValue(content)
	ed.Focus()

	view := ed.View()
	lines := strings.Split(view, "\n")
	for i, line := range lines {
		w := lipgloss.Width(line)
		if w != 50 {
			t.Errorf("line %d: width=%d (want 50)", i, w)
		}
	}
}

func TestSoftLineWrap(t *testing.T) {
	ed := New()
	ed.ShowLineNumbers = true
	ed.ReadOnly = true
	ed.BgColor = lipgloss.Color("#000000")
	ed.LineNumStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#666666"))

	// Width 30, gutter ~3 chars ("_1 "), so text width = 27.
	// Line "abcdefghij..." (40 chars) should wrap into 2 visual rows.
	ed.SetWidth(30)
	ed.SetHeight(10)
	ed.SetValue("short\n" + strings.Repeat("a", 40) + "\nend")

	view := ed.View()
	lines := strings.Split(view, "\n")

	// Should have: line 1 (short), line 2 wrap-1, line 2 wrap-2, line 3 (end), + 6 empty = 10
	if len(lines) != 10 {
		t.Errorf("expected 10 visual rows, got %d", len(lines))
	}
	// Every visual row must be exactly 30 wide.
	for i, line := range lines {
		w := lipgloss.Width(line)
		if w != 30 {
			t.Errorf("visual row %d: width=%d (want 30)", i, w)
		}
	}

	// The long line should produce a continuation row with blank gutter.
	// First visual row of long line should have "2" in gutter, second should not.
	if len(lines) >= 3 {
		// Row index 1 = first segment of buffer line 2 → should have "2" in gutter
		if !strings.Contains(lines[1], "2") {
			t.Errorf("visual row 1 should show line number 2, got: %q", lines[1])
		}
	}
}

func TestWrapPlain(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  int // number of segments
	}{
		{"hello", 10, 1},
		{"hello world!!!", 5, 3}, // "hello", " worl", "d!!!"
		{"", 5, 1},
		{strings.Repeat("x", 20), 7, 3}, // 7+7+6
	}
	for _, tc := range cases {
		got := wrapPlain(tc.in, tc.width)
		if len(got) != tc.want {
			t.Errorf("wrapPlain(%q, %d) = %d segments, want %d: %v",
				tc.in, tc.width, len(got), tc.want, got)
		}
	}
}

func TestVisualRowCount(t *testing.T) {
	ed := New()
	ed.SetWidth(10) // no line numbers, so textWidth = 10
	ed.SetHeight(20)
	ed.SetValue("short\n" + strings.Repeat("a", 25) + "\nend")

	// Line 0: "short" (5 chars) → 1 visual row
	// Line 1: 25 chars → ceil(25/10) = 3 visual rows
	// Line 2: "end" (3 chars) → 1 visual row
	// Total: 5
	got := ed.visualRowCount()
	if got != 5 {
		t.Errorf("visualRowCount() = %d, want 5", got)
	}
}

func TestExpandTabs(t *testing.T) {
	cases := []struct {
		in   string
		want int // visual width (all chars are ASCII, so rune count = display width)
	}{
		{"\thello", 4 + 5},       // 1 tab (4 spaces) + "hello"
		{"\t\thello", 4 + 4 + 5}, // 2 tabs + "hello"
		{"ab\tc", 2 + 2 + 1},     // "ab" then tab to col 4, then "c"
		{"no tabs", 7},
	}
	for _, tc := range cases {
		got := expandTabs(tc.in)
		w := len([]rune(got))
		if w != tc.want {
			t.Errorf("expandTabs(%q) width=%d, want %d (got %q)", tc.in, w, tc.want, got)
		}
	}
}
