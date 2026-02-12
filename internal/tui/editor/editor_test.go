package editor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
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
		fmt.Printf("line %d: width=%d\n", i, w)
	}
}

func TestExpandTabs(t *testing.T) {
	cases := []struct {
		in   string
		want int // visual width (all chars are ASCII, so rune count = display width)
	}{
		{"\thello", 4 + 5},      // 1 tab (4 spaces) + "hello"
		{"\t\thello", 4 + 4 + 5}, // 2 tabs + "hello"
		{"ab\tc", 2 + 2 + 1},    // "ab" then tab to col 4, then "c"
		{"no tabs", 7},
	}
	for _, tc := range cases {
		got := expandTabs(tc.in)
		w := len([]rune(got))
		if w != tc.want {
			t.Errorf("expandTabs(%q) width=%d, want %d (got %q)", tc.in, w, tc.want, got)
		}
		fmt.Printf("expandTabs(%q) -> %q (width %d)\n", tc.in, got, w)
	}
}
