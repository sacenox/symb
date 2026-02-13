package tui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/xonecas/symb/internal/mcp_tools"
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
	var cmds []tea.Cmd
	x, y := mouseXY(msg)

	// --- Divider drag -------------------------------------------------------
	switch ev := msg.(type) {
	case tea.MouseClickMsg:
		if ev.Button == tea.MouseLeft && inRect(x, y, m.layout.div) {
			m.resizingPane = true
		}
	case tea.MouseReleaseMsg:
		m.resizingPane = false
	default:
	}
	if m.resizingPane {
		if _, ok := msg.(tea.MouseMotionMsg); ok {
			if x >= minPaneWidth && x <= m.width-minPaneWidth {
				m.divX = x
				m.layout = generateLayout(m.width, m.height, m.divX)
				m.updateComponentSizes()
			}
			return m, nil
		}
	}

	// --- Focus switching on click -------------------------------------------
	if click, ok := msg.(tea.MouseClickMsg); ok && click.Button == tea.MouseLeft {
		switch {
		case inRect(x, y, m.layout.editor):
			m.setFocus(focusEditor)
			m.agentInput.ClearSelection()
			m.convSel = nil
		case inRect(x, y, m.layout.input):
			m.setFocus(focusInput)
			m.editor.ClearSelection()
			m.convSel = nil
		}
	}

	// --- Editor: forward with original coords (left pane starts at 0) -------
	if inRect(x, y, m.layout.editor) {
		var cmd tea.Cmd
		m.editor, cmd = m.editor.Update(msg)
		cmds = append(cmds, cmd)
		return m, tea.Batch(cmds...)
	}

	// --- Input: translate coords to component-local -------------------------
	if inRect(x, y, m.layout.input) {
		translated := m.translateMouse(msg, m.layout.input.Min.X, m.layout.input.Min.Y)
		var cmd tea.Cmd
		m.agentInput, cmd = m.agentInput.Update(translated)
		cmds = append(cmds, cmd)
		return m, tea.Batch(cmds...)
	}

	// --- Conversation: scroll + selection -----------------------------------
	if inRect(x, y, m.layout.conv) {
		convH := m.layout.conv.Dy()
		lines := m.wrappedConvLines()
		totalLines := len(lines)

		switch ev := msg.(type) {
		case tea.MouseClickMsg:
			if ev.Button != tea.MouseLeft || totalLines == 0 {
				break
			}
			cp := m.convPosFromScreen(x, y, totalLines)
			m.convDragging = true
			m.convSel = &convSelection{anchor: cp, active: cp}
			m.editor.ClearSelection()
			m.agentInput.ClearSelection()

		case tea.MouseMotionMsg:
			if m.convDragging && m.convSel != nil && totalLines > 0 {
				m.convSel.active = m.convPosFromScreen(x, y, totalLines)
			}

		case tea.MouseReleaseMsg:
			m.convDragging = false
			if m.convSel != nil && m.convSel.empty() {
				clickedLine := m.convPosFromScreen(x, y, totalLines).line
				m.convSel = nil
				if cmd := m.handleConvClick(clickedLine); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}

		case tea.MouseWheelMsg:
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
	}

	return m, tea.Batch(cmds...)
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

// handleConvClick resolves a click on a wrapped conversation line.
// If the line belongs to a tool result entry, the full content is opened in
// the editor. Otherwise, if the line contains a file path reference
// (path/to/file.go:123), that file is opened in the editor.
func (m *Model) handleConvClick(wrappedLine int) tea.Cmd {
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

	// Tool result: open the source file or fall back to raw content
	if entry.kind == entryToolResult {
		if entry.filePath != "" {
			if content, err := os.ReadFile(entry.filePath); err == nil {
				m.editor.SetValue(string(content))
				m.editor.Language = mcp_tools.DetectLanguage(entry.filePath)
				m.setFocus(focusEditor)
				return nil
			}
		}
		// Fallback: show raw tool result text
		if entry.full != "" {
			m.editor.SetValue(entry.full)
			m.editor.Language = "text"
			m.setFocus(focusEditor)
			return nil
		}
	}

	// Try to extract a file path from the clicked line's plain text
	lines := m.wrappedConvLines()
	if wrappedLine >= len(lines) {
		return nil
	}
	plain := ansi.Strip(lines[wrappedLine])
	return m.tryOpenFilePath(plain)
}

// tryOpenFilePath looks for a file:line reference in text and opens it in the editor.
func (m *Model) tryOpenFilePath(text string) tea.Cmd {
	matches := filePathRe.FindStringSubmatch(text)
	if matches == nil {
		return nil
	}
	path := matches[1]
	lineNum := 0
	if matches[2] != "" {
		lineNum, _ = strconv.Atoi(matches[2])
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil
	}

	// Restrict to files within the working directory
	wd, err := os.Getwd()
	if err != nil {
		return nil
	}
	rel, err := filepath.Rel(wd, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return nil
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return nil
	}

	language := mcp_tools.DetectLanguage(path)
	m.editor.SetValue(string(content))
	m.editor.Language = language
	if lineNum > 0 {
		m.editor.GotoLine(lineNum)
	}
	m.setFocus(focusEditor)
	return nil
}
