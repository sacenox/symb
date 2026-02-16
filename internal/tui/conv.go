package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/xonecas/symb/internal/constants"
	"github.com/xonecas/symb/internal/highlight"
	"github.com/xonecas/symb/internal/provider"
)

// ---------------------------------------------------------------------------
// Conversation entry helpers
// ---------------------------------------------------------------------------

// convWidth returns the usable width of the conversation pane.
func (m Model) convWidth() int { return m.layout.conv.Dx() }

// highlightMarkdown highlights a full markdown text block via Chroma.
// The entire text is tokenised as one unit so multi-line constructs
// (fenced code blocks, block quotes, etc.) maintain correct state.
// Rendering cost is bounded by the frame-loop tick (~16ms).
func highlightMarkdown(text string, fallback lipgloss.Style) []string {
	hl := highlight.Highlight(text, "markdown", constants.SyntaxTheme, palette.Bg)
	if hl == text {
		// Chroma produced no highlighting; apply fallback per line.
		raw := strings.Split(text, "\n")
		out := make([]string, len(raw))
		for i, line := range raw {
			out[i] = fallback.Render(line)
		}
		return out
	}
	return highlight.SplitLines(hl)
}

// styledLines applies a lipgloss style to each line in a multi-line text.
// No wrapping — lines are stored raw for later wrapping at render time.
func styledLines(text string, style lipgloss.Style) []string {
	raw := strings.Split(text, "\n")
	out := make([]string, len(raw))
	for i, l := range raw {
		out[i] = style.Render(l)
	}
	return out
}

// textEntries converts styled display strings into plain convEntry values.
func textEntries(lines ...string) []convEntry {
	out := make([]convEntry, len(lines))
	for i, l := range lines {
		out[i] = convEntry{display: l, kind: entryText}
	}
	return out
}

// appendConv appends entries and returns whether we were at bottom
// (for sticky scroll).
func (m *Model) appendConv(entries ...convEntry) bool {
	atBottom := m.scrollOffset == 0
	m.convEntries = append(m.convEntries, entries...)
	return atBottom
}

// appendText is a convenience to append plain text entries.
func (m *Model) appendText(lines ...string) bool {
	return m.appendConv(textEntries(lines...)...)
}

// rebuildStreamEntries replaces any existing streaming entries with fresh
// styled entries from the current streamingReasoning and streamingContent.
// Wrapping is deferred to View() — this only updates convEntries.
func (m *Model) rebuildStreamEntries() {
	// Remove old streaming entries.
	if m.streamEntryStart >= 0 && m.streamEntryStart <= len(m.convEntries) {
		m.convEntries = m.convEntries[:m.streamEntryStart]
	}

	if m.streamingReasoning != "" {
		m.convEntries = append(m.convEntries, textEntries(styledLines(m.streamingReasoning, m.styles.Muted)...)...)
	}
	if m.streamingContent != "" {
		m.convEntries = append(m.convEntries, textEntries(highlightMarkdown(m.streamingContent, m.styles.Text)...)...)
	}
}

// wrappedConvLines wraps all conversation entries to the current convWidth.
// Cached for the current frame — cleared at the start of each Update cycle.
func (m *Model) wrappedConvLines() []string {
	if m.frameLines != nil {
		return m.frameLines
	}
	w := m.convWidth()
	lines := make([]string, 0, len(m.convEntries))
	source := make([]int, 0, len(m.convEntries))
	for i, entry := range m.convEntries {
		if entry.display == "" {
			lines = append(lines, "")
			source = append(source, i)
		} else {
			wrapped := wrapANSI(entry.display, w)
			for range wrapped {
				source = append(source, i)
			}
			lines = append(lines, wrapped...)
		}
	}
	m.convLineSource = source
	m.frameLines = lines
	return lines
}

// formatTokens formats a token count for display (e.g. 1234 -> "1.2k").
func formatTokens(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// makeSeparator builds a right-aligned timestamp/token separator line.
func (m Model) makeSeparator(dur string, ts string, tokIn, tokOut, totalTok, ctxTok int) string {
	var label string
	if tokIn > 0 || tokOut > 0 {
		label = fmt.Sprintf("%s %s ↓ %s ↑ %s Σ %s ◔ %s", dur, ts, formatTokens(tokIn), formatTokens(tokOut), formatTokens(totalTok), formatTokens(ctxTok))
	} else {
		label = dur + " " + ts
	}
	w := m.convWidth()
	pad := w - lipgloss.Width(label)
	if pad < 0 {
		pad = 0
	}
	return m.styles.Dim.Render(strings.Repeat(" ", pad) + label)
}

// makeUndoEntry creates convEntries for the undo control: the separator line
// plus a right-aligned "undo" line below it.
// sepDisplay is the styled separator text to restore if the undo entry is demoted.
func (m Model) makeUndoEntry(sepDisplay string) []convEntry {
	undoLabel := "undo"
	w := m.convWidth()
	pad := w - lipgloss.Width(undoLabel)
	if pad < 0 {
		pad = 0
	}
	undoLine := m.styles.Dim.Render(strings.Repeat(" ", pad) + undoLabel)
	return []convEntry{
		{display: sepDisplay, kind: entrySeparator, full: sepDisplay},
		{display: undoLine, kind: entryUndo, full: sepDisplay},
	}
}

// visibleStartLine returns the index of the first visible wrapped conversation line.
func (m *Model) visibleStartLine() int {
	lines := m.wrappedConvLines()
	total := len(lines)
	visible := m.layout.conv.Dy()
	if total <= visible {
		return 0
	}
	start := total - visible - m.scrollOffset
	if start < 0 {
		return 0
	}
	return start
}

// historyConvEntries rebuilds conversation display entries from loaded history.
func historyConvEntries(msgs []provider.Message) []convEntry {
	sty := DefaultStyles()
	var entries []convEntry
	for _, msg := range msgs {
		switch msg.Role {
		case "system":
			// not displayed
		case "user":
			if msg.Content == "" {
				continue
			}
			entries = append(entries, textEntries(highlightMarkdown(msg.Content, sty.Text)...)...)
		case roleAssistant:
			if msg.Content != "" {
				entries = append(entries, textEntries(highlightMarkdown(msg.Content, sty.Text)...)...)
			}
			for _, tc := range msg.ToolCalls {
				line := formatToolCall(tc)
				entries = append(entries, convEntry{display: line, kind: entryToolResult, full: line})
			}
		case "tool":
			// tool results stored as content; show abbreviated
			if msg.Content != "" {
				lines := strings.SplitN(msg.Content, "\n", 2)
				display := lines[0]
				if len(display) > 200 {
					display = display[:200] + "…"
				}
				entries = append(entries, convEntry{display: display, kind: entryToolResult, full: msg.Content})
			}
		}
	}
	return entries
}
