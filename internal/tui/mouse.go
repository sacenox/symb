package tui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/xonecas/symb/internal/highlight"
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

	// --- Divider drag -------------------------------------------------------
	if done, handled := m.handleDividerDrag(msg, x, y); handled {
		return done, nil
	}

	// --- Focus switching on click -------------------------------------------
	m.handleFocusClick(msg, x, y)

	// --- Hover: clear when outside conv area --------------------------------
	if _, isMotion := msg.(tea.MouseMotionMsg); isMotion && !inRect(x, y, m.layout.conv) {
		m.hoverConvLine = -1
	}

	// --- Editor: forward with original coords (left pane starts at 0) -------
	if inRect(x, y, m.layout.editor) {
		var cmd tea.Cmd
		m.editor, cmd = m.editor.Update(msg)
		return m, cmd
	}

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

// handleDividerDrag tracks divider click/release and processes drag motion.
// Returns (model, true) if the event was consumed by the divider.
func (m *Model) handleDividerDrag(msg tea.MouseMsg, x, y int) (Model, bool) {
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
			return *m, true
		}
	}
	return Model{}, false
}

// handleFocusClick switches focus when clicking in the editor or input panes.
func (m *Model) handleFocusClick(msg tea.MouseMsg, x, y int) {
	click, ok := msg.(tea.MouseClickMsg)
	if !ok || click.Button != tea.MouseLeft {
		return
	}
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
			m.editor.ClearSelection()
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
	}
	if totalLines > 0 {
		lineIdx := m.visibleStartLine() + (y - m.layout.conv.Min.Y)
		if lineIdx >= 0 && lineIdx < totalLines && m.isClickableLine(lineIdx) {
			m.hoverConvLine = lineIdx
		} else {
			m.hoverConvLine = -1
		}
	}
}

func (m *Model) handleConvRelease(x, y, totalLines int) tea.Cmd {
	m.convDragging = false
	if m.convSel != nil && m.convSel.empty() {
		clickedLine := m.convPosFromScreen(x, y, totalLines).line
		m.convSel = nil
		return m.handleConvClick(clickedLine)
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

// isClickableLine returns true if the wrapped line at lineIdx is clickable
// (tool result entry or contains a file path reference).
func (m *Model) isClickableLine(lineIdx int) bool {
	lines := m.wrappedConvLines() // ensures convLineSource is also fresh
	src := m.convLineSource
	if lineIdx < 0 || lineIdx >= len(src) {
		return false
	}
	entryIdx := src[lineIdx]
	if entryIdx < 0 || entryIdx >= len(m.convEntries) {
		return false
	}
	if m.convEntries[entryIdx].kind == entryToolResult {
		return true
	}
	if m.convEntries[entryIdx].kind == entryUndo {
		return true
	}
	if lineIdx >= len(lines) {
		return false
	}
	plain := ansi.Strip(lines[lineIdx])
	return filePathRe.MatchString(plain)
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

	// Undo control: emit undoMsg
	if entry.kind == entryUndo {
		return func() tea.Msg { return undoMsg{} }
	}

	// Tool result: open the source file or fall back to raw content
	if entry.kind == entryToolResult {
		if entry.filePath != "" {
			absPath, _ := filepath.Abs(entry.filePath)
			if content, err := os.ReadFile(entry.filePath); err == nil {
				m.editor.SetValue(string(content))
				m.editor.Language = highlight.DetectLanguage(entry.filePath)
				m.editor.SetGutterMarkers(GitFileMarkers(m.ctx, entry.filePath))
				m.editor.DiagnosticLines = nil
				m.editorFilePath = absPath
				if entry.line > 0 {
					m.editor.GotoLine(entry.line)
				}
				m.setFocus(focusEditor)
				return nil
			}
		}
		// Fallback: show raw tool result text
		if entry.full != "" {
			m.editor.SetValue(entry.full)
			m.editor.Language = "text"
			m.editor.SetGutterMarkers(nil)
			m.editor.DiagnosticLines = nil
			m.editorFilePath = ""
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

	language := highlight.DetectLanguage(path)
	m.editor.SetValue(string(content))
	m.editor.Language = language
	m.editor.SetGutterMarkers(GitFileMarkers(m.ctx, path))
	m.editor.DiagnosticLines = nil
	m.editorFilePath = absPath
	if lineNum > 0 {
		m.editor.GotoLine(lineNum)
	}
	m.setFocus(focusEditor)
	return nil
}
