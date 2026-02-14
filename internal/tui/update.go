package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/xonecas/symb/internal/mcp_tools"
	"github.com/xonecas/symb/internal/provider"
)

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	// -- Window resize -------------------------------------------------------
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.divX == 0 {
			m.divX = m.width / 2
		}
		// Constrain divider
		if m.divX < minPaneWidth {
			m.divX = minPaneWidth
		}
		if m.divX > m.width-minPaneWidth {
			m.divX = m.width - minPaneWidth
		}
		m.layout = generateLayout(m.width, m.height, m.divX)
		m.updateComponentSizes()

	// -- Clipboard read response (from ctrl+shift+v) -------------------------
	case tea.ClipboardMsg:
		text := msg.String()
		if text != "" {
			switch m.focus {
			case focusInput:
				m.agentInput.DeleteSelection()
				m.agentInput.InsertText(text)
			case focusEditor:
				m.editor.DeleteSelection()
				m.editor.InsertText(text)
			}
		}
		return m, nil

	// -- Bracketed paste (terminal paste via ctrl+v / middle-click) ----------
	case tea.PasteMsg:
		text := msg.Content
		if text != "" {
			switch m.focus {
			case focusInput:
				m.agentInput.DeleteSelection()
				m.agentInput.InsertText(text)
			case focusEditor:
				m.editor.DeleteSelection()
				m.editor.InsertText(text)
			}
		}
		return m, nil

	// -- Mouse ---------------------------------------------------------------
	case tea.MouseMsg:
		return m.handleMouse(msg)

	// -- Keyboard ------------------------------------------------------------
	case tea.KeyPressMsg:
		switch msg.Keystroke() {
		case "ctrl+c":
			m.cancel()
			return m, tea.Quit

		case "ctrl+shift+c":
			if cmd := m.copySelection(); cmd != nil {
				return m, cmd
			}
			return m, nil

		case "ctrl+shift+v":
			return m, tea.ReadClipboard

		case "esc":
			if m.focus == focusInput {
				m.agentInput.Blur()
			} else {
				m.editor.Blur()
			}
			return m, nil
		case "enter":
			if m.focus == focusInput && m.agentInput.Value() != "" {
				userMsg := m.agentInput.Value()
				m.agentInput.Reset()
				return m, m.sendToLLM(userMsg)
			}
		}

	// -- LLM messages --------------------------------------------------------
	case llmUserMsg:
		now := time.Now()
		m.history = append(m.history, provider.Message{
			Role: "user", Content: msg.content, CreatedAt: now,
		})
		m.appendText(styledLines(msg.content, m.styles.Text)...)
		m.appendText("")
		sep := m.makeSeparator("0s", now.Format("15:04:05"))
		wasBottom := m.appendText(sep)
		m.appendText("")
		if wasBottom {
			m.scrollOffset = 0
		}
		return m, tea.Batch(m.processLLM(), m.waitForLLMUpdate())

	case llmReasoningDeltaMsg:
		if !m.streaming {
			m.streaming = true
			m.streamEntryStart = len(m.convEntries)
			m.streamingReasoning = ""
			m.streamingContent = ""
		}
		m.streamingReasoning += msg.content
		m.rebuildStreamEntries()
		if m.scrollOffset == 0 {
			m.scrollOffset = 0 // stay pinned
		}
		return m, m.waitForLLMUpdate()

	case llmContentDeltaMsg:
		if !m.streaming {
			m.streaming = true
			m.streamEntryStart = len(m.convEntries)
			m.streamingReasoning = ""
			m.streamingContent = ""
		}
		m.streamingContent += msg.content
		m.rebuildStreamEntries()
		if m.scrollOffset == 0 {
			m.scrollOffset = 0 // stay pinned
		}
		return m, m.waitForLLMUpdate()

	case llmHistoryMsg:
		m.history = append(m.history, msg.msg)
		return m, m.waitForLLMUpdate()

	case llmAssistantMsg:
		// Finalize streaming state: replace streaming entries with final styled content
		if m.streaming {
			m.streaming = false
			// Remove streaming entries
			if m.streamEntryStart >= 0 && m.streamEntryStart <= len(m.convEntries) {
				m.convEntries = m.convEntries[:m.streamEntryStart]
			}
			m.streamEntryStart = -1
			m.streamingReasoning = ""
			m.streamingContent = ""
			m.convLines = nil // invalidate cache
		}

		if msg.reasoning != "" {
			wasBottom := m.appendText(styledLines(msg.reasoning, m.styles.Muted)...)
			m.appendText("")
			if wasBottom {
				m.scrollOffset = 0
			}
		}
		if msg.content != "" {
			wasBottom := m.appendText(styledLines(msg.content, m.styles.Text)...)
			m.appendText("")
			if wasBottom {
				m.scrollOffset = 0
			}
		}
		for _, tc := range msg.toolCalls {
			entry := m.styles.ToolArrow.Render("→") + "  " + m.styles.ToolCall.Render(tc.Name+"(...)")
			wasBottom := m.appendText(entry)
			if wasBottom {
				m.scrollOffset = 0
			}
		}
		return m, m.waitForLLMUpdate()

	case llmToolResultMsg:
		// Extract file path from tool result header (Opened/Edited/Created)
		var filePath string
		if sm := toolResultFileRe.FindStringSubmatch(msg.content); sm != nil {
			filePath = sm[1]
		}

		lines := strings.Split(msg.content, "\n")
		preview := lines
		truncated := false
		if len(lines) > maxPreviewLines {
			preview = lines[:maxPreviewLines]
			truncated = true
		}

		arrow := m.styles.ToolArrow.Render("←") + "  "
		var wasBottom bool
		for i, line := range preview {
			display := m.styles.Dim.Render(line)
			if i == 0 {
				display = arrow + display
				wasBottom = m.appendConv(convEntry{display: display, kind: entryToolResult, filePath: filePath, full: msg.content})
			} else {
				m.appendConv(convEntry{display: display, kind: entryToolResult, filePath: filePath, full: msg.content})
			}
		}
		if truncated {
			hint := fmt.Sprintf("  ... %d more lines (click to view)", len(lines)-maxPreviewLines)
			m.appendConv(convEntry{display: m.styles.Muted.Render(hint), kind: entryToolResult, filePath: filePath, full: msg.content})
		}
		if wasBottom {
			m.scrollOffset = 0
		}
		return m, m.waitForLLMUpdate()

	case llmErrorMsg:
		m.appendText("", m.styles.Error.Render("Error: "+msg.err.Error()), "")
		return m, nil

	case llmDoneMsg:
		m.appendText("")
		sep := m.makeSeparator(msg.duration.Round(time.Second).String(), msg.timestamp)
		m.appendText(sep)
		return m, nil

	case mcp_tools.OpenForUserMsg:
		m.editor.SetValue(msg.Content)
		m.editor.Language = msg.Language
		m.editor.SetLineBg(nil)
		m.editor.DiagnosticLines = nil // Clear stale diagnostics on file switch.
		markers := mcp_tools.GitFileMarkers(m.ctx, msg.FilePath)
		m.editor.SetGutterMarkers(markers)
		m.editorFilePath = msg.AbsPath
		m.setFocus(focusEditor)
		return m, nil

	case LSPDiagnosticsMsg:
		// Only apply diagnostics if they match the file currently in the editor.
		if msg.FilePath == m.editorFilePath {
			m.editor.DiagnosticLines = msg.Lines
		}
		return m, nil

	case UpdateToolsMsg:
		m.mcpTools = msg.Tools
		return m, nil
	}

	// Always tick spinner
	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	cmds = append(cmds, cmd)

	// Forward non-mouse messages to focused component
	if _, isMouse := msg.(tea.MouseMsg); !isMouse {
		m.editor, cmd = m.editor.Update(msg)
		cmds = append(cmds, cmd)
		m.agentInput, cmd = m.agentInput.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

// updateComponentSizes pushes layout dimensions to sub-models.
func (m *Model) updateComponentSizes() {
	m.editor.SetWidth(m.layout.editor.Dx())
	m.editor.SetHeight(m.layout.editor.Dy())
	m.agentInput.SetWidth(m.layout.input.Dx() - 2) // padding for border
	m.agentInput.SetHeight(inputRows)
}
