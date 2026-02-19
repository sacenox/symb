package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ---------------------------------------------------------------------------
// Mouse filter — throttle high-frequency events at program level.
// ---------------------------------------------------------------------------

var lastMouseEvent time.Time

// MouseEventFilter rate-limits wheel and motion events (15 ms).
// Pass to tea.WithFilter. Never drops clicks or releases.
func MouseEventFilter(_ tea.Model, msg tea.Msg) tea.Msg {
	switch msg.(type) {
	case tea.MouseWheelMsg, tea.MouseMotionMsg:
		now := time.Now()
		if now.Sub(lastMouseEvent) < 15*time.Millisecond {
			return nil
		}
		lastMouseEvent = now
	}
	return msg
}

// ---------------------------------------------------------------------------
// Mouse handling — dialog-first when we add dialogs, then focus-based.
// Coordinate translation via layout rects.
// ---------------------------------------------------------------------------

// mouseXY extracts X, Y from any mouse message via the MouseMsg interface.
func mouseXY(msg tea.MouseMsg) (int, int) {
	m := msg.Mouse()
	return m.X, m.Y
}

func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	x, y := mouseXY(msg)

	// --- Input: translate coords to component-local -------------------------
	if inRect(x, y, m.layout.input) {
		translated := m.translateMouse(msg, m.layout.input.Min.X, m.layout.input.Min.Y)
		var cmd tea.Cmd
		m.agentInput, cmd = m.agentInput.Update(translated)
		return m, cmd
	}

	// --- Conversation: scroll + selection -----------------------------------
	if inRect(x, y, m.layout.conv) {
		return m, m.handleConvMouse(msg, x, y)
	}

	return m, nil
}

// handleConvMouse handles mouse events within the conversation pane.
func (m *Model) handleConvMouse(msg tea.MouseMsg, x, y int) tea.Cmd {
	lines := m.wrappedConvLines()
	totalLines := len(lines)

	switch ev := msg.(type) {
	case tea.MouseClickMsg:
		if ev.Button == tea.MouseLeft && totalLines > 0 {
			cp := m.convPosFromScreen(x, y, totalLines)
			m.convDragging = true
			m.convSel = &convSelection{anchor: cp, active: cp}
			m.agentInput.ClearSelection()
		}
	case tea.MouseMotionMsg:
		m.handleConvMotion(x, y, totalLines)
	case tea.MouseReleaseMsg:
		return m.handleConvRelease(x, y, totalLines)
	case tea.MouseWheelMsg:
		m.handleConvWheel(ev, totalLines)
	}
	return nil
}

func (m *Model) handleConvMotion(x, y, totalLines int) {
	if m.convDragging && m.convSel != nil && totalLines > 0 {
		m.convSel.active = m.convPosFromScreen(x, y, totalLines)
	} else if !m.convDragging && m.convSel != nil {
		m.convSel = nil
	}

}

func (m *Model) handleConvRelease(x, y, totalLines int) tea.Cmd {
	m.convDragging = false
	if m.convSel != nil && m.convSel.empty() {
		cp := m.convPosFromScreen(x, y, totalLines)
		m.convSel = nil
		// Ignore clicks on empty space past the last wrapped line.
		startLine := m.visibleStartLine()
		screenLine := startLine + (y - m.layout.conv.Min.Y)
		if screenLine >= totalLines {
			return nil
		}
		return m.handleConvClick(cp.line, cp.col)
	}
	return nil
}

func (m *Model) handleConvWheel(ev tea.MouseWheelMsg, totalLines int) {
	convH := m.layout.conv.Dy()
	if ev.Button == tea.MouseWheelUp {
		maxScroll := totalLines - convH
		if maxScroll < 0 {
			maxScroll = 0
		}
		m.scrollOffset = min(m.scrollOffset+5, maxScroll)
	} else if ev.Button == tea.MouseWheelDown {
		m.scrollOffset = max(m.scrollOffset-5, 0)
	}
}

// convPosFromScreen converts screen x,y to a convPos.
func (m *Model) convPosFromScreen(x, y, totalLines int) convPos {
	startLine := m.visibleStartLine()
	clickedLine := startLine + (y - m.layout.conv.Min.Y)
	if clickedLine < 0 {
		clickedLine = 0
	}
	if clickedLine >= totalLines {
		clickedLine = totalLines - 1
	}
	clickedCol := x - m.layout.conv.Min.X
	if clickedCol < 0 {
		clickedCol = 0
	}
	return convPos{line: clickedLine, col: clickedCol}
}

// translateMouse offsets a mouse message's coordinates for child components.
func (m Model) translateMouse(msg tea.MouseMsg, offX, offY int) tea.Msg {
	switch ev := msg.(type) {
	case tea.MouseClickMsg:
		ev.X -= offX
		ev.Y -= offY
		return ev
	case tea.MouseMotionMsg:
		ev.X -= offX
		ev.Y -= offY
		return ev
	case tea.MouseReleaseMsg:
		ev.X -= offX
		ev.Y -= offY
		return ev
	case tea.MouseWheelMsg:
		ev.X -= offX
		ev.Y -= offY
		return ev
	}
	return msg
}

// isClickableLine returns true if the wrapped line at lineIdx is clickable.
// Tool result entries (first line only, which has the [view] button) and undo
// entries are always clickable.
func (m *Model) isClickableLine(lineIdx int) bool {
	m.wrappedConvLines() // ensures convLineSource is also fresh
	src := m.convLineSource
	if lineIdx < 0 || lineIdx >= len(src) {
		return false
	}
	entryIdx := src[lineIdx]
	if entryIdx < 0 || entryIdx >= len(m.convEntries) {
		return false
	}
	entry := m.convEntries[entryIdx]
	switch entry.kind {
	case entryToolResult:
		// Only the first wrapped line (containing [view]) is clickable.
		if lineIdx > 0 && src[lineIdx-1] == entryIdx {
			return false
		}
		return true
	case entryUndo:
		return true
	case entryToolDiag, entryToolCall, entrySeparator:
		return false
	default:
		return false
	}
}

// handleConvClick resolves a click on a wrapped conversation line.
// Tool result [view] buttons open the relevant content in the editor.
// Undo buttons trigger an undo.
func (m *Model) handleConvClick(wrappedLine, col int) tea.Cmd {
	m.wrappedConvLines() // ensure convLineSource is fresh
	src := m.convLineSource
	if wrappedLine < 0 || wrappedLine >= len(src) {
		return nil
	}
	entryIdx := src[wrappedLine]
	if entryIdx < 0 || entryIdx >= len(m.convEntries) {
		return nil
	}
	entry := m.convEntries[entryIdx]

	switch entry.kind {
	case entryUndo:
		if m.isClickOnCenteredLabel(entry.display, col) {
			return func() tea.Msg { return undoMsg{} }
		}
		return nil

	case entryToolResult:
		// Only trigger on the [view] label at the end of the line.
		if m.isClickOnViewLabel(entry.display, col) {
			return m.handleToolResultView(entry)
		}
		return nil

	case entryToolDiag, entryToolCall, entrySeparator:
		return nil

	default:
		return nil
	}
}

// isClickOnCenteredLabel checks whether a column falls within a centered
// label's visible text area in the conversation pane.
func (m *Model) isClickOnCenteredLabel(display string, col int) bool {
	rw := m.convWidth()
	lw := lipgloss.Width(display)
	pad := (rw - lw) / 2
	if pad < 0 {
		pad = 0
	}
	return col >= pad && col < pad+lw
}

// isClickOnViewLabel checks whether a column falls on the "view" label
// at the end of a tool result line.
func (m *Model) isClickOnViewLabel(display string, col int) bool {
	const viewLabel = "view"
	lw := lipgloss.Width(display)
	viewStart := lw - len(viewLabel)
	if viewStart < 0 {
		viewStart = 0
	}
	return col >= viewStart && col < lw
}

// handleToolResultView returns a Cmd that opens the tool view modal.
func (m *Model) handleToolResultView(entry convEntry) tea.Cmd {
	title := entry.toolName
	if title == "" {
		title = "Tool Result"
	}
	return func() tea.Msg {
		return openToolViewMsg{title: title, content: entry.full}
	}
}
