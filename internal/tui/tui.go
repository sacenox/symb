package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Model is the application model
type Model struct {
	width  int
	height int
}

// New creates a new TUI model
func New() Model {
	return Model{}
}

// Init initializes the TUI (required by BubbleTea)
func (m Model) Init() tea.Cmd {
	return nil
}

// Update handles messages and updates state (required by BubbleTea)
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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

	return m, nil
}

// View renders the UI (required by BubbleTea)
func (m Model) View() string {
	if m.width == 0 {
		return "Initializing..."
	}

	return "╭─────────────────────────────────────┬────────────────────────────────────╮\n" +
		"│ Editor Pane                         │ Agent Smith                       │\n" +
		"│                                     │                                    │\n" +
		"│ Press q or Ctrl+C to quit           │                                    │\n" +
		"╰─────────────────────────────────────┴────────────────────────────────────╯\n"
}
