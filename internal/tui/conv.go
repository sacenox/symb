package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// ---------------------------------------------------------------------------
// Conversation entry helpers
// ---------------------------------------------------------------------------

// convWidth returns the usable width of the conversation pane.
func (m Model) convWidth() int { return m.layout.conv.Dx() }

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
// (for sticky scroll). Invalidates the wrapped-lines cache.
func (m *Model) appendConv(entries ...convEntry) bool {
	atBottom := m.scrollOffset == 0
	m.convEntries = append(m.convEntries, entries...)
	m.convLines = nil // invalidate cache
	return atBottom
}

// appendText is a convenience to append plain text entries.
func (m *Model) appendText(lines ...string) bool {
	return m.appendConv(textEntries(lines...)...)
}

// rebuildStreamEntries replaces any existing streaming entries with fresh
// styled entries from the current streamingReasoning and streamingContent.
// Called on each delta to reflect incremental updates.
func (m *Model) rebuildStreamEntries() {
	// Remove old streaming entries
	if m.streamEntryStart >= 0 && m.streamEntryStart <= len(m.convEntries) {
		m.convEntries = m.convEntries[:m.streamEntryStart]
	}

	if m.streamingReasoning != "" {
		m.convEntries = append(m.convEntries, textEntries(styledLines(m.streamingReasoning, m.styles.Muted)...)...)
	}
	if m.streamingContent != "" {
		m.convEntries = append(m.convEntries, textEntries(styledLines(m.streamingContent, m.styles.Text)...)...)
	}
	m.convLines = nil // invalidate cache
}

// wrappedConvLines returns the conversation wrapped to the current convWidth.
// Cached — only recomputed when entries change (nil) or width changes.
func (m *Model) wrappedConvLines() []string {
	w := m.convWidth()
	if m.convLines != nil && m.convCachedW == w {
		return m.convLines
	}
	m.convCachedW = w
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
	m.convLines = lines
	m.convLineSource = source
	return m.convLines
}

// makeSeparator builds a timestamp separator line.
func (m Model) makeSeparator(dur string, ts string) string {
	label := dur + " " + ts + " "
	fill := m.convWidth() - lipgloss.Width(label)
	if fill < 0 {
		fill = 0
	}
	return m.styles.Dim.Render(label + strings.Repeat("─", fill))
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
