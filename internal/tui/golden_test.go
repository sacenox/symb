package tui

import (
	"regexp"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/golden"
)

// stripANSI removes ANSI escape codes for golden file comparison
func stripANSI(s string) string {
	ansiRe := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	return ansiRe.ReplaceAllString(s, "")
}

func TestLayout(t *testing.T) {
	tests := []struct {
		name   string
		width  int
		height int
	}{
		{"80x24", 80, 24},
		{"120x40", 120, 40},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New(nil, nil, nil, "test-model", nil, "test-session", nil, nil, nil)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: tt.width, Height: tt.height})
			m = updated.(Model)

			output := m.renderContent()

			t.Run("ANSI", func(t *testing.T) {
				golden.RequireEqual(t, []byte(output))
			})

			t.Run("Stripped", func(t *testing.T) {
				stripped := stripANSI(output)
				golden.RequireEqual(t, []byte(stripped))
			})
		})
	}
}
