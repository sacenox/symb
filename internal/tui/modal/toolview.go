package modal

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ToolView is a simple read-only modal that displays a tool call and its result.
type ToolView struct {
	title   string
	content string
	scroll  int
	colors  Colors
}

// NewToolView creates a new tool viewer modal.
func NewToolView(title, content string, colors Colors) ToolView {
	return ToolView{
		title:   title,
		content: content,
		colors:  colors,
	}
}

// HandleMsg processes key events. Returns ActionClose when the modal should close.
func (t *ToolView) HandleMsg(msg tea.Msg) (Action, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.Keystroke() {
		case "esc", "q", "enter":
			return ActionClose{}, nil
		case "up", "k":
			if t.scroll > 0 {
				t.scroll--
			}
		case "down", "j":
			t.scroll++
		case "pgup":
			t.scroll -= 10
			if t.scroll < 0 {
				t.scroll = 0
			}
		case "pgdown":
			t.scroll += 10
		}
	case tea.MouseWheelMsg:
		if msg.Button == tea.MouseWheelUp {
			if t.scroll > 0 {
				t.scroll--
			}
		} else if msg.Button == tea.MouseWheelDown {
			t.scroll++
		}
	}
	return nil, nil
}

// View renders the modal centered in the terminal at appWidth x appHeight.
func (t *ToolView) View(appWidth, appHeight int) string {
	w := appWidth * 80 / 100
	h := appHeight * 80 / 100
	if w < 30 {
		w = 30
	}
	if h < 8 {
		h = 8
	}

	innerW := w - 6 // border (2) + padding (2)
	if innerW < 10 {
		innerW = 10
	}

	bg := lipgloss.Color(t.colors.Bg)
	fg := lipgloss.Color(t.colors.Fg)
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(t.colors.Dim)).Background(bg)
	fgStyle := lipgloss.NewStyle().Foreground(fg).Background(bg)

	// Wrap content lines to innerW.
	rawLines := strings.Split(t.content, "\n")
	var wrapped []string
	for _, line := range rawLines {
		if lipgloss.Width(line) <= innerW {
			wrapped = append(wrapped, line)
		} else {
			// Simple character-level wrap.
			for len(line) > 0 {
				if len(line) <= innerW {
					wrapped = append(wrapped, line)
					break
				}
				wrapped = append(wrapped, line[:innerW])
				line = line[innerW:]
			}
		}
	}

	// Title row + divider = 2 rows overhead inside the box.
	listH := h - 4 // border top/bottom (2) + title (1) + divider (1)
	if listH < 1 {
		listH = 1
	}

	// Clamp scroll.
	maxScroll := len(wrapped) - listH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if t.scroll > maxScroll {
		t.scroll = maxScroll
	}

	// Build content string.
	titleLine := fgStyle.Bold(true).Render(truncate(t.title, innerW))
	divider := dimStyle.Render(strings.Repeat("\u2500", innerW))

	var sb strings.Builder
	sb.WriteString(titleLine)
	sb.WriteByte('\n')
	sb.WriteString(divider)

	end := t.scroll + listH
	if end > len(wrapped) {
		end = len(wrapped)
	}
	for _, l := range wrapped[t.scroll:end] {
		sb.WriteByte('\n')
		sb.WriteString(fgStyle.Render(padRight(l, innerW)))
	}
	// Pad remaining lines so the box has consistent height.
	rendered := end - t.scroll
	for i := rendered; i < listH; i++ {
		sb.WriteByte('\n')
		sb.WriteString(fgStyle.Render(strings.Repeat(" ", innerW)))
	}

	// Scroll hint in bottom-right corner of the last line if needed.
	hintStyle := dimStyle
	var hint string
	if t.scroll > 0 && t.scroll < maxScroll {
		hint = "↑↓"
	} else if t.scroll > 0 {
		hint = "↑"
	} else if maxScroll > 0 {
		hint = "↓"
	}
	_ = hint
	_ = hintStyle

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(t.colors.Border)).
		BorderBackground(bg).
		Foreground(fg).
		Background(bg).
		Padding(0, 1).
		Width(w - 2).
		Render(sb.String())

	return lipgloss.Place(appWidth, appHeight, lipgloss.Center, lipgloss.Center, box,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(bg)))
}

func truncate(s string, maxW int) string {
	if lipgloss.Width(s) <= maxW {
		return s
	}
	if maxW <= 3 {
		return s[:maxW]
	}
	return s[:maxW-3] + "..."
}
