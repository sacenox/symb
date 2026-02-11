package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Model is the application model
type Model struct {
	width   int
	height  int
	spinner spinner.Model
}

// New creates a new TUI model
func New() Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(ColorMatrix) // Matrix green
	return Model{
		spinner: s,
	}
}

// Init initializes the TUI (required by BubbleTea)
func (m Model) Init() tea.Cmd {
	return m.spinner.Tick
}

// Update handles messages and updates state (required by BubbleTea)
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	}

	// Update spinner
	m.spinner, cmd = m.spinner.Update(msg)
	return m, cmd
}

// View renders the UI (required by BubbleTea)
func (m Model) View() string {
	if m.width == 0 {
		return ""
	}

	// Split width in half for left/right panes
	halfWidth := m.width / 2

	// Content height = total - top border - status separator - status bar - bottom border
	contentHeight := m.height - 4

	var b strings.Builder
	borderStyle := lipgloss.NewStyle().Foreground(ColorBorder)

	// Top border: ╭───...───┬───...───╮
	b.WriteString(borderStyle.Render("╭"))
	b.WriteString(borderStyle.Render(strings.Repeat("─", halfWidth-1)))
	b.WriteString(borderStyle.Render("┬"))
	b.WriteString(borderStyle.Render(strings.Repeat("─", m.width-halfWidth-2)))
	b.WriteString(borderStyle.Render("╮"))
	b.WriteString("\n")

	// Content rows
	for i := 0; i < contentHeight; i++ {
		b.WriteString(borderStyle.Render("│"))
		b.WriteString(strings.Repeat(" ", halfWidth-1))
		b.WriteString(borderStyle.Render("│"))
		b.WriteString(strings.Repeat(" ", m.width-halfWidth-2))
		b.WriteString(borderStyle.Render("│"))
		b.WriteString("\n")
	}

	// Status separator: ├───...───┴───...───┤
	b.WriteString(borderStyle.Render("├"))
	b.WriteString(borderStyle.Render(strings.Repeat("─", halfWidth-1)))
	b.WriteString(borderStyle.Render("┴"))
	b.WriteString(borderStyle.Render(strings.Repeat("─", m.width-halfWidth-2)))
	b.WriteString(borderStyle.Render("┤"))
	b.WriteString("\n")

	// Status bar: │ master* │<spaces>spinner │
	statusLeft := borderStyle.Render("│") + " master* " + borderStyle.Render("│")
	spinnerView := strings.TrimSpace(m.spinner.View())
	statusRight := " " + borderStyle.Render("│")
	// Use lipgloss.Width for accurate display width
	leftWidth := lipgloss.Width(statusLeft)
	rightWidth := lipgloss.Width(statusRight)
	spinnerWidth := lipgloss.Width(spinnerView)
	spacesNeeded := m.width - leftWidth - spinnerWidth - rightWidth
	b.WriteString(statusLeft)
	b.WriteString(strings.Repeat(" ", spacesNeeded))
	b.WriteString(spinnerView)
	b.WriteString(statusRight)
	b.WriteString("\n")

	// Bottom border: ╰───...───╯
	b.WriteString(borderStyle.Render("╰"))
	b.WriteString(borderStyle.Render(strings.Repeat("─", m.width-2)))
	b.WriteString(borderStyle.Render("╯"))

	return b.String()
}
