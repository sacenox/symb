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

// makeSeparator builds a timestamp/token separator label.
// Centering is applied at render time so it adapts to resizes.
func makeSeparator(sty Styles, dur, ts string, tokIn, tokOut, totalTok, ctxTok int) string {
	var label string
	if tokIn > 0 || tokOut > 0 {
		label = fmt.Sprintf("%s  %s  ↓ %s ↑ %s Σ %s ◔ %s", ts, dur, formatTokens(tokIn), formatTokens(tokOut), formatTokens(totalTok), formatTokens(ctxTok))
	} else {
		label = ts + "  " + dur
	}
	return sty.Dim.Render(label)
}

// makeUndoEntry creates convEntries for the undo control: the separator line
// plus a centered [undo] button below it. Centering is applied at render time.
// sepDisplay is the styled separator text to restore if the undo entry is demoted.
func (m Model) makeUndoEntry(sepDisplay string) []convEntry {
	return []convEntry{
		{display: sepDisplay, kind: entrySeparator, full: sepDisplay},
		{display: m.styles.Clickable.Render("undo"), kind: entryUndo, full: sepDisplay},
	}
}

// isCentered returns true if the wrapped line at lineIdx belongs to a
// separator or undo entry that should be centered in the conversation pane.
// Caller must ensure convLineSource is fresh (call wrappedConvLines first).
func (m Model) isCentered(lineIdx int) bool {
	src := m.convLineSource
	if lineIdx < 0 || lineIdx >= len(src) {
		return false
	}
	entryIdx := src[lineIdx]
	if entryIdx < 0 || entryIdx >= len(m.convEntries) {
		return false
	}
	k := m.convEntries[entryIdx].kind
	return k == entrySeparator || k == entryUndo
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
func historyConvEntries(msgs []provider.Message, sty Styles) []convEntry {
	var entries []convEntry
	for _, msg := range msgs {
		switch msg.Role {
		case "system":
			// not displayed
		case "user":
			if msg.Content == "" {
				continue
			}
			entries = append(entries, convEntry{display: "", kind: entryText})
			entries = append(entries, textEntries(highlightMarkdown(msg.Content, sty.Text)...)...)
			entries = append(entries, convEntry{display: "", kind: entryText})
		case roleAssistant:
			if msg.Reasoning != "" {
				entries = append(entries, textEntries(styledLines(msg.Reasoning, sty.Muted)...)...)
				entries = append(entries, convEntry{display: "", kind: entryText})
			}
			if msg.Content != "" {
				entries = append(entries, textEntries(highlightMarkdown(msg.Content, sty.Text)...)...)
				entries = append(entries, convEntry{display: "", kind: entryText})
			}
			for _, tc := range msg.ToolCalls {
				display := sty.ToolArrow.Render("→ ") + sty.BgFill.Render("  ") + sty.ToolCall.Render(formatToolCall(tc))
				entries = append(entries, convEntry{display: display, kind: entryToolCall})
			}
		case "tool":
			if msg.Content != "" {
				lines := strings.SplitN(msg.Content, "\n", 2)
				body := lines[0]
				if len(body) > 200 {
					body = body[:200] + "…"
				}
				arrow := sty.ToolArrow.Render("← ") + sty.BgFill.Render("  ")
				display := arrow + sty.Dim.Render(body) + sty.BgFill.Render("  ") + sty.Clickable.Render("view")

				var filePath string
				if sm := toolResultFileRe.FindStringSubmatch(msg.Content); sm != nil {
					filePath = sm[1]
				}
				entries = append(entries, convEntry{
					display:  display,
					kind:     entryToolResult,
					filePath: filePath,
					full:     msg.Content,
				})
			}
		}
	}
	return entries
}
