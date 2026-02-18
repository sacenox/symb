package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// renderStatusBar writes the status separator and bar.
func (m Model) renderStatusBar(b *strings.Builder, bgFill lipgloss.Style) {
	divX := m.layout.div.Min.X
	b.WriteString(m.styles.Border.Render(strings.Repeat("─", divX)))
	b.WriteString(m.styles.Border.Render("┴"))
	b.WriteString(m.styles.Border.Render(strings.Repeat("─", m.width-divX-1)))
	b.WriteByte('\n')

	// -- Left segments --
	var leftParts []string

	// Git branch + dirty
	if m.gitBranch != "" {
		branch := m.gitBranch
		if m.gitDirty {
			branch += "*"
		}
		leftParts = append(leftParts, m.styles.StatusText.Render(" "+branch))
	}

	// LSP diagnostics for current editor file
	if m.lspErrors > 0 || m.lspWarnings > 0 {
		var diags []string
		if m.lspErrors > 0 {
			diags = append(diags, m.styles.Error.Render(fmt.Sprintf("✗ %d", m.lspErrors)))
		}
		if m.lspWarnings > 0 {
			diags = append(diags, m.styles.Warning.Render(fmt.Sprintf("⚠ %d", m.lspWarnings)))
		}
		leftParts = append(leftParts, strings.Join(diags, m.styles.StatusText.Render(" ")))
	} else if m.editorFilePath != "" {
		leftParts = append(leftParts, m.styles.StatusText.Render(m.editorFilePath))
	}

	left := strings.Join(leftParts, m.styles.StatusText.Render("  "))

	// -- Right segments --
	var rightParts []string

	// Network error (truncated)
	if m.lastNetError != "" {
		errText := m.lastNetError
		if len(errText) > 30 {
			errText = errText[:30] + "…"
		}
		rightParts = append(rightParts, m.styles.Error.Render("✗ "+errText))
	}

	// Provider config name + model
	providerLabel := m.providerConfigName
	if m.currentModelName != "" {
		providerLabel += "/" + m.currentModelName
	}
	rightParts = append(rightParts, m.styles.StatusText.Render(providerLabel))

	// Animated braille spinner — red on error, accent otherwise
	frame := brailleFrames[m.spinFrame%len(brailleFrames)]
	if m.lastNetError != "" {
		frame = m.styles.Error.Render(frame)
	} else {
		frame = lipgloss.NewStyle().Background(ColorBg).Foreground(ColorHighlight).Render(frame)
	}
	rightParts = append(rightParts, frame)

	right := strings.Join(rightParts, m.styles.StatusText.Render(" "))

	// -- Compose: left + gap + right + trailing space --
	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	gap := m.width - leftW - rightW - 1
	if gap < 0 {
		gap = 0
	}
	b.WriteString(left)
	b.WriteString(bgFill.Render(strings.Repeat(" ", gap)))
	b.WriteString(right)
	b.WriteString(bgFill.Render(" "))
}
