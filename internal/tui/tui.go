package tui

import (
	"context"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/xonecas/symb/internal/llm"
	"github.com/xonecas/symb/internal/mcp"
	"github.com/xonecas/symb/internal/provider"
	editorarea "github.com/xonecas/symb/internal/tui/textarea"
)

// Model is the application model
type Model struct {
	width      int
	height     int
	spinner    spinner.Model
	editor     editorarea.Model // Left pane: code editor
	agentInput textarea.Model   // Right pane: agent input box

	// LLM components
	provider     provider.Provider
	mcpProxy     *mcp.Proxy
	mcpTools     []mcp.Tool
	history      []provider.Message
	conversation []string     // Formatted conversation log for display
	updateChan   chan tea.Msg // Channel for streaming LLM updates
	ctx          context.Context
	cancel       context.CancelFunc
}

// New creates a new TUI model
func New(prov provider.Provider, proxy *mcp.Proxy, tools []mcp.Tool) Model {
	cursorStyle := lipgloss.NewStyle().Foreground(ColorMatrix) // Matrix green

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = cursorStyle

	// Left pane: code editor with syntax highlighting
	editor := editorarea.New()
	editor.ShowLineNumbers = true
	editor.Prompt = ""
	editor.FocusedStyle.CursorLine = lipgloss.NewStyle().Background(lipgloss.NoColor{})
	editor.FocusedStyle.LineNumber = lipgloss.NewStyle().Foreground(ColorBorder)
	editor.Cursor.Style = cursorStyle
	editor.Language = "markdown"

	// Right pane: agent input (official bubbles textarea)
	agentInput := textarea.New()
	agentInput.Placeholder = "Type a message..."
	agentInput.SetHeight(3)
	agentInput.Prompt = ""
	agentInput.FocusedStyle.CursorLine = lipgloss.NewStyle().Background(lipgloss.NoColor{})
	agentInput.ShowLineNumbers = false
	agentInput.Cursor.Style = cursorStyle
	agentInput.Focus()

	updateChan := make(chan tea.Msg, 500)
	ctx, cancel := context.WithCancel(context.Background())

	return Model{
		spinner:      s,
		editor:       editor,
		agentInput:   agentInput,
		provider:     prov,
		mcpProxy:     proxy,
		mcpTools:     tools,
		history:      []provider.Message{},
		conversation: []string{},
		updateChan:   updateChan,
		ctx:          ctx,
		cancel:       cancel,
	}
}

// Init initializes the TUI (required by BubbleTea)
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, editorarea.Blink)
}

// llmUserMsg adds user message to conversation (ELM Msg)
type llmUserMsg struct {
	content string
}

// llmAssistantMsg adds assistant message (ELM Msg)
type llmAssistantMsg struct {
	content string
}

// llmToolCallMsg adds tool call (ELM Msg)
type llmToolCallMsg struct {
	name string
}

// llmToolResultMsg adds tool result (ELM Msg)
type llmToolResultMsg struct {
	content string
}

// llmDoneMsg adds timestamp separator (ELM Msg)
type llmDoneMsg struct {
	duration  time.Duration
	timestamp string
}

// llmHistoryMsg updates history with new message (ELM Msg)
type llmHistoryMsg struct {
	msg provider.Message
}

// llmErrorMsg handles LLM errors (ELM Msg)
type llmErrorMsg struct {
	err error
}

// sendToLLM sends user message to LLM (ELM Cmd)
func (m Model) sendToLLM(userInput string) tea.Cmd {
	return func() tea.Msg {
		// Immediately show user message
		return llmUserMsg{content: userInput}
	}
}

// waitForLLMUpdate waits for next message from LLM (ELM Cmd)
func (m Model) waitForLLMUpdate() tea.Cmd {
	return func() tea.Msg {
		return <-m.updateChan
	}
}

// processLLM runs LLM turn in background (ELM Cmd)
func (m Model) processLLM() tea.Cmd {
	return func() tea.Msg {
		go func() {
			startTime := time.Now()

			opts := llm.ProcessTurnOptions{
				Provider:      m.provider,
				Proxy:         m.mcpProxy,
				Tools:         m.mcpTools,
				History:       m.history,
				MaxToolRounds: 20,
				OnMessage: func(msg provider.Message) {
					// Send message to update history
					m.updateChan <- llmHistoryMsg{msg: msg}

					if msg.Role == "assistant" {
						// Send tool calls
						for _, tc := range msg.ToolCalls {
							m.updateChan <- llmToolCallMsg{name: tc.Name}
						}
						// Send assistant content
						if msg.Content != "" {
							m.updateChan <- llmAssistantMsg{content: msg.Content}
						}
					} else if msg.Role == "tool" {
						// Send tool result
						firstLine := strings.Split(msg.Content, "\n")[0]
						m.updateChan <- llmToolResultMsg{content: firstLine}
					}
				},
			}

			err := llm.ProcessTurn(m.ctx, opts)
			if err != nil {
				m.updateChan <- llmErrorMsg{err: err}
				return
			}

			// Send done message
			m.updateChan <- llmDoneMsg{
				duration:  time.Since(startTime),
				timestamp: startTime.Format("15:04"),
			}
		}()

		return nil
	}
}

// Update handles messages and updates state (required by BubbleTea)
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.cancel() // Cancel any running goroutines
			return m, tea.Quit
		case "esc":
			if m.agentInput.Focused() {
				m.agentInput.Blur()
			}
			return m, nil
		case "enter":
			if m.agentInput.Focused() && m.agentInput.Value() != "" {
				userMsg := m.agentInput.Value()
				m.agentInput.Reset()
				return m, m.sendToLLM(userMsg)
			}
			return m, nil
		}

	case llmUserMsg:
		now := time.Now()
		// Add user message to history
		m.history = append(m.history, provider.Message{
			Role:      "user",
			Content:   msg.content,
			CreatedAt: now,
		})
		// Format and display user message first
		grayStyle := lipgloss.NewStyle().Foreground(ColorGray)
		for _, line := range strings.Split(msg.content, "\n") {
			m.conversation = append(m.conversation, grayStyle.Render(line))
		}
		m.conversation = append(m.conversation, "")
		// Add timestamp separator after message
		timestamp := now.Format("15:04")
		rightPaneWidth := m.width/2 - 1
		dashCount := rightPaneWidth - len("0s "+timestamp+" ")
		if dashCount < 0 {
			dashCount = 0
		}
		separator := lipgloss.NewStyle().Foreground(ColorGray).Render(
			"0s " + timestamp + " " + strings.Repeat("─", dashCount),
		)
		m.conversation = append(m.conversation, separator)
		m.conversation = append(m.conversation, "")
		// Start LLM processing and wait for updates
		return m, tea.Batch(m.processLLM(), m.waitForLLMUpdate())

	case llmHistoryMsg:
		// Update history with message from LLM
		m.history = append(m.history, msg.msg)
		return m, m.waitForLLMUpdate() // Continue listening

	case llmAssistantMsg:
		for _, line := range strings.Split(msg.content, "\n") {
			m.conversation = append(m.conversation, line)
		}
		m.conversation = append(m.conversation, "")
		return m, m.waitForLLMUpdate() // Continue listening

	case llmToolCallMsg:
		m.conversation = append(m.conversation, "→  "+msg.name+"(...)")
		return m, m.waitForLLMUpdate() // Continue listening

	case llmToolResultMsg:
		m.conversation = append(m.conversation, "←  "+msg.content)
		return m, m.waitForLLMUpdate() // Continue listening

	case llmErrorMsg:
		// Display error message
		m.conversation = append(m.conversation, "")
		errLine := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff0000")).Render("Error: " + msg.err.Error())
		m.conversation = append(m.conversation, errLine)
		m.conversation = append(m.conversation, "")
		return m, nil // Done listening

	case llmDoneMsg:
		// Add timestamp separator (blank line first, then separator)
		m.conversation = append(m.conversation, "")
		durationStr := msg.duration.Round(time.Second).String()
		rightPaneWidth := m.width/2 - 1
		dashCount := rightPaneWidth - len(durationStr+" "+msg.timestamp+" ")
		if dashCount < 0 {
			dashCount = 0
		}
		separator := lipgloss.NewStyle().Foreground(ColorGray).Render(
			durationStr + " " + msg.timestamp + " " + strings.Repeat("─", dashCount),
		)
		m.conversation = append(m.conversation, separator)
		return m, nil // Done listening

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		halfWidth := m.width / 2
		contentHeight := m.height - 2 // -2 for status separator and status bar

		// Left pane: editor
		m.editor.SetWidth(halfWidth - 1)
		m.editor.SetHeight(contentHeight)

		// Right pane: agent input (3 rows tall + 2 for border)
		m.agentInput.SetWidth(m.width - halfWidth - 4) // -3 for borders and padding
		m.agentInput.SetHeight(3)
	}

	// Update spinner
	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	cmds = append(cmds, cmd)

	// Update editor
	m.editor, cmd = m.editor.Update(msg)
	cmds = append(cmds, cmd)

	// Update agent input
	m.agentInput, cmd = m.agentInput.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

// View renders the UI (required by BubbleTea)
func (m Model) View() string {
	if m.width == 0 {
		return ""
	}

	// Split width in half for left/right panes
	halfWidth := m.width / 2

	// Content height = total - status separator - status bar
	contentHeight := m.height - 2

	var b strings.Builder
	borderStyle := lipgloss.NewStyle().Foreground(ColorBorder)

	// Render editor in left pane
	editorView := m.editor.View()
	editorLines := strings.Split(editorView, "\n")

	// Render agent input box (3 rows + 2 for borders = 5 total)
	agentInputView := m.agentInput.View()
	agentInputLines := strings.Split(agentInputView, "\n")

	// Content rows
	rightPaneWidth := m.width - halfWidth - 1
	for i := 0; i < contentHeight; i++ {
		// Left pane: editor
		if i < len(editorLines) {
			line := editorLines[i]
			lineWidth := lipgloss.Width(line)
			padding := halfWidth - lineWidth
			if padding < 0 {
				padding = 0
			}
			b.WriteString(line)
			b.WriteString(strings.Repeat(" ", padding))
		} else {
			b.WriteString(strings.Repeat(" ", halfWidth))
		}

		// Middle divider (or T connection on separator row)
		separatorRow := contentHeight - 4 // 1 row for separator
		if i == separatorRow {
			b.WriteString(borderStyle.Render("├"))
		} else {
			b.WriteString(borderStyle.Render("│"))
		}

		// Right pane: conversation area (top), separator, input at bottom
		inputStartRow := contentHeight - 3 // 3 rows for input
		if i < separatorRow {
			// Conversation area - show conversation log
			// Calculate which line to show (scroll to show newest at bottom)
			totalLines := len(m.conversation)
			visibleLines := separatorRow
			startLine := 0
			if totalLines > visibleLines {
				startLine = totalLines - visibleLines
			}
			lineIdx := startLine + i

			if lineIdx < totalLines {
				line := m.conversation[lineIdx]
				lineWidth := lipgloss.Width(line)
				if lineWidth > rightPaneWidth {
					line = line[:rightPaneWidth]
					lineWidth = rightPaneWidth
				}
				b.WriteString(line)
				b.WriteString(strings.Repeat(" ", rightPaneWidth-lineWidth))
			} else {
				b.WriteString(strings.Repeat(" ", rightPaneWidth))
			}
		} else if i == separatorRow {
			// Separator line
			b.WriteString(borderStyle.Render(strings.Repeat("─", rightPaneWidth)))
		} else {
			// Input area (last 3 rows)
			lineIdx := i - inputStartRow
			if lineIdx < len(agentInputLines) {
				line := agentInputLines[lineIdx]
				lineWidth := lipgloss.Width(line)
				if lineWidth > rightPaneWidth {
					line = line[:rightPaneWidth]
					lineWidth = rightPaneWidth
				}
				b.WriteString(line)
				b.WriteString(strings.Repeat(" ", rightPaneWidth-lineWidth))
			} else {
				b.WriteString(strings.Repeat(" ", rightPaneWidth))
			}
		}

		b.WriteString("\n")
	}

	// Status separator: ───...───┴───...───
	b.WriteString(borderStyle.Render(strings.Repeat("─", halfWidth)))
	b.WriteString(borderStyle.Render("┴"))
	b.WriteString(borderStyle.Render(strings.Repeat("─", m.width-halfWidth-1)))
	b.WriteString("\n")

	// Status bar: master* │<spaces>spinner
	statusTextStyle := lipgloss.NewStyle().Foreground(ColorGray)
	statusLeft := statusTextStyle.Render(" gitbranch/working dir")
	spinnerView := strings.TrimSpace(m.spinner.View())
	// Use lipgloss.Width for accurate display width
	leftWidth := lipgloss.Width(statusLeft)
	spinnerWidth := lipgloss.Width(spinnerView)
	spacesNeeded := m.width - leftWidth - spinnerWidth - 1
	b.WriteString(statusLeft)
	b.WriteString(strings.Repeat(" ", spacesNeeded))
	b.WriteString(spinnerView)
	b.WriteString(" ")

	return b.String()
}
