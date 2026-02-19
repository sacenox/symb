package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/xonecas/symb/internal/provider"
)

// TestToolViewModalOpensOnViewClick verifies that clicking the [view] button
// on a tool result entry opens the tool view modal.
func TestToolViewModalOpensOnViewClick(t *testing.T) {
	initTheme("vulcan")
	m := New(nil, nil, nil, "test", nil, "s", nil, nil, nil, "p", nil, nil, nil, provider.Options{}, "vulcan")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)

	// Inject a tool result entry.
	entry := convEntry{
		display:  m.styles.ToolArrow.Render("←") + m.styles.BgFill.Render("  ") + m.styles.Dim.Render("Read foo.go") + m.styles.BgFill.Render("  ") + m.styles.Clickable.Render("view"),
		kind:     entryToolResult,
		full:     "Read foo.go\nsome content",
		toolName: "Read",
	}
	m.convEntries = append(m.convEntries, entry)
	m.frameLines = nil

	// Place the click at the [view] label: last 4 visible chars of the display.
	lines := m.wrappedConvLines()
	if len(lines) == 0 {
		t.Fatal("no conv lines")
	}
	convX := m.layout.conv.Min.X
	convY := m.layout.conv.Min.Y

	// col within conv pane pointing at "view" (last 4 chars of display).
	import_lipgloss_width := func(s string) int {
		// inline: strip ANSI and count runes
		n := 0
		inEsc := false
		for _, r := range s {
			if inEsc {
				if r == 'm' {
					inEsc = false
				}
				continue
			}
			if r == '\x1b' {
				inEsc = true
				continue
			}
			n++
		}
		return n
	}
	lw := import_lipgloss_width(entry.display)
	viewCol := lw - 2 // somewhere inside "view"

	clickX := convX + viewCol
	clickY := convY

	// MouseClickMsg sets convSel; MouseReleaseMsg fires the click handler returning a Cmd.
	u1, _ := m.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	m = u1.(Model)
	u2, cmd := m.Update(tea.MouseReleaseMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	m = u2.(Model)

	// The cmd carries openToolViewMsg; dispatch it.
	if cmd == nil {
		t.Fatal("expected a Cmd from the release, got nil")
	}
	msg := cmd()
	u3, _ := m.Update(msg)
	m = u3.(Model)

	if m.toolViewModal == nil {
		t.Fatal("toolViewModal is nil after dispatching openToolViewMsg")
	}
}
