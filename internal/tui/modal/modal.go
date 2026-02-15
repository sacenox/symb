package modal

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Action is the result of handling a message. nil means no action.
type Action any

// ActionClose signals the modal should be dismissed.
type ActionClose struct{}

// ActionSelect signals an item was chosen.
type ActionSelect struct{ Item Item }

// Item is a single entry in the list.
type Item struct {
	Name string
	Desc string
}

// SearchFunc is called with the current query to produce results.
type SearchFunc func(query string) []Item

// Colors holds the theme colors for the modal.
type Colors struct {
	Fg     string
	Bg     string
	Dim    string
	SelFg  string
	SelBg  string
	Border string
}

const (
	debounceDelay = 250 * time.Millisecond

	keyDown      = "down"
	keyBackspace = "backspace"
)

// debounceMsg is sent after the debounce timer fires.
type debounceMsg struct{ seq int }

// Model is a generic input+list modal.
type Model struct {
	input    []rune
	cursor   int
	items    []Item
	selected int
	inList   bool // true = list focused, false = input focused

	searchFn SearchFunc
	seq      int // debounce sequence counter

	colors Colors

	// Prompt shown before the input text.
	Prompt string
}

// New creates a modal with the given search function.
func New(searchFn SearchFunc, prompt string, colors Colors) Model {
	m := Model{
		searchFn: searchFn,
		Prompt:   prompt,
		colors:   colors,
	}
	// Initial search with empty query.
	m.items = searchFn("")
	return m
}

// DebounceCmd returns a tea.Cmd that fires after the debounce delay.
func (m *Model) DebounceCmd() tea.Cmd {
	seq := m.seq
	return tea.Tick(debounceDelay, func(time.Time) tea.Msg {
		return debounceMsg{seq: seq}
	})
}

// HandleMsg processes a tea.Msg and returns an optional Action.
// The second return is a tea.Cmd the parent must dispatch (for debounce).
func (m *Model) HandleMsg(msg tea.Msg) (Action, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case debounceMsg:
		if msg.seq == m.seq {
			m.items = m.searchFn(string(m.input))
			m.selected = 0
			m.inList = false
		}
		return nil, nil
	}
	return nil, nil
}

func (m *Model) handleKey(msg tea.KeyPressMsg) (Action, tea.Cmd) {
	switch msg.Keystroke() {
	case "esc":
		return ActionClose{}, nil
	case "enter":
		return m.handleEnter()
	case "up", keyDown:
		m.handleNav(msg.Keystroke())
		return nil, nil
	case keyBackspace, "delete", "ctrl+u", "ctrl+k":
		return nil, m.handleDelete(msg.Keystroke())
	case "left", "right", "home", "end", "ctrl+a", "ctrl+e":
		m.handleCursor(msg.Keystroke())
		return nil, nil
	}

	// Rune input.
	if !m.inList && msg.Text != "" {
		for _, r := range msg.Text {
			m.input = append(m.input[:m.cursor], append([]rune{r}, m.input[m.cursor:]...)...)
			m.cursor++
		}
		m.seq++
		return nil, m.DebounceCmd()
	}

	return nil, nil
}

func (m *Model) handleEnter() (Action, tea.Cmd) {
	if len(m.items) == 0 {
		return nil, nil
	}
	idx := m.selected
	if idx >= len(m.items) {
		idx = 0
	}
	return ActionSelect{Item: m.items[idx]}, nil
}

func (m *Model) handleNav(key string) {
	switch key {
	case "up":
		if m.inList {
			if m.selected > 0 {
				m.selected--
			} else {
				m.inList = false
			}
		}
	case keyDown:
		if !m.inList {
			if len(m.items) > 0 {
				m.inList = true
				m.selected = 0
			}
		} else if m.selected < len(m.items)-1 {
			m.selected++
		}
	}
}

func (m *Model) handleDelete(key string) tea.Cmd {
	switch key {
	case keyBackspace:
		if m.cursor > 0 {
			m.input = append(m.input[:m.cursor-1], m.input[m.cursor:]...)
			m.cursor--
			m.seq++
			return m.DebounceCmd()
		}
	case "delete":
		if m.cursor < len(m.input) {
			m.input = append(m.input[:m.cursor], m.input[m.cursor+1:]...)
			m.seq++
			return m.DebounceCmd()
		}
	case "ctrl+u":
		m.input = m.input[m.cursor:]
		m.cursor = 0
		m.seq++
		return m.DebounceCmd()
	case "ctrl+k":
		m.input = m.input[:m.cursor]
		m.seq++
		return m.DebounceCmd()
	}
	return nil
}

func (m *Model) handleCursor(key string) {
	if m.inList {
		return
	}
	switch key {
	case "left":
		if m.cursor > 0 {
			m.cursor--
		}
	case "right":
		if m.cursor < len(m.input) {
			m.cursor++
		}
	case "home", "ctrl+a":
		m.cursor = 0
	case "end", "ctrl+e":
		m.cursor = len(m.input)
	}
}

// View renders the modal at the given app width and height.
func (m *Model) View(appWidth, appHeight int) string {
	w := appWidth * 80 / 100
	h := appHeight * 80 / 100
	if w < 30 {
		w = 30
	}
	if h < 8 {
		h = 8
	}

	innerW := w - 6 // border + padding
	if innerW < 10 {
		innerW = 10
	}

	prompt := m.Prompt
	if prompt == "" {
		prompt = "> "
	}

	inputLine := m.renderInput(prompt)
	listHeight := h - 4 // border top/bottom + input + divider
	if listHeight < 1 {
		listHeight = 1
	}

	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.colors.Dim))
	divider := dimStyle.Render(strings.Repeat("\u2500", innerW))
	listLines := m.renderList(innerW, listHeight)

	content := inputLine + "\n" + divider
	for _, l := range listLines {
		content += "\n" + l
	}

	bg := lipgloss.Color(m.colors.Bg)
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(m.colors.Border)).
		BorderBackground(bg).
		Foreground(lipgloss.Color(m.colors.Fg)).
		Background(bg).
		Padding(0, 1).
		Width(w - 2).
		Render(content)

	return lipgloss.Place(appWidth, appHeight, lipgloss.Center, lipgloss.Center, box,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(bg)))
}

func (m *Model) renderInput(prompt string) string {
	if m.inList {
		return prompt + string(m.input)
	}
	before := string(m.input[:m.cursor])
	cursorStyle := lipgloss.NewStyle().Reverse(true)
	cursorChar := " "
	after := ""
	if m.cursor < len(m.input) {
		cursorChar = string(m.input[m.cursor])
		after = string(m.input[m.cursor+1:])
	}
	return prompt + before + cursorStyle.Render(cursorChar) + after
}

func (m *Model) renderList(innerW, listHeight int) []string {
	scrollOff := 0
	if m.selected >= listHeight {
		scrollOff = m.selected - listHeight + 1
	}

	bg := lipgloss.Color(m.colors.Bg)
	dimStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.colors.Dim)).
		Background(bg)
	selStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.colors.SelFg)).
		Background(lipgloss.Color(m.colors.SelBg))

	var lines []string
	for i := scrollOff; i < len(m.items) && len(lines) < listHeight; i++ {
		item := m.items[i]
		if i == m.selected && m.inList {
			lines = append(lines, selStyle.Render(padRight(item.Name, innerW)))
		} else {
			line := item.Name
			if item.Desc != "" {
				line += dimStyle.Render("  " + item.Desc)
			}
			lines = append(lines, padRight(line, innerW))
		}
	}

	for len(lines) < listHeight {
		lines = append(lines, strings.Repeat(" ", innerW))
	}
	return lines
}

func padRight(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-len(s))
}
