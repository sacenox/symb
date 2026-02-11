package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Model is the application model
type Model struct {
	width    int
	height   int
	spinner  spinner.Model
	textarea textarea.Model
}

// New creates a new TUI model
func New() Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(ColorMatrix) // Matrix green

	ta := textarea.New()
	ta.ShowLineNumbers = true
	ta.Prompt = ""
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle().Background(lipgloss.NoColor{})
	ta.FocusedStyle.LineNumber = lipgloss.NewStyle().Foreground(ColorBorder)
	ta.Cursor.Style = lipgloss.NewStyle().Foreground(ColorMatrix)
	ta.Focus()

	return Model{
		spinner:  s,
		textarea: ta,
	}
}

// Init initializes the TUI (required by BubbleTea)
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, textarea.Blink)
}

// Update handles messages and updates state (required by BubbleTea)
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			if m.textarea.Focused() {
				m.textarea.Blur()
			}
			return m, nil
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		// Update textarea size to fit left pane
		halfWidth := m.width / 2
		contentHeight := m.height - 2      // -2 for status separator and status bar
		m.textarea.SetWidth(halfWidth - 1) // -1 for center divider
		m.textarea.SetHeight(contentHeight)
	}

	// Update spinner
	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	cmds = append(cmds, cmd)

	// Update textarea
	m.textarea, cmd = m.textarea.Update(msg)
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

	// Render textarea in left pane
	textareaView := m.textarea.View()
	textareaLines := strings.Split(textareaView, "\n")

	// Content rows
	for i := 0; i < contentHeight; i++ {
		// Left pane: textarea
		if i < len(textareaLines) {
			line := textareaLines[i]
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

		b.WriteString(borderStyle.Render("│"))

		// Right pane: empty for now
		b.WriteString(strings.Repeat(" ", m.width-halfWidth-1))

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
