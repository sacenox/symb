package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/xonecas/symb/internal/filesearch"
	"github.com/xonecas/symb/internal/tui/modal"
)

func (m *Model) openFileModal() {
	searchFn := func(query string) []modal.Item {
		if query == "" {
			return nil
		}
		results, err := m.searcher.Search(context.Background(), filesearch.Options{
			Pattern:       query,
			ContentSearch: false,
			MaxResults:    50,
		})
		if err != nil {
			return nil
		}
		items := make([]modal.Item, len(results))
		for i, r := range results {
			items[i] = modal.Item{Name: r.Path}
		}
		return items
	}
	md := modal.New(searchFn, "Open: ", modal.Colors{
		Fg:     palette.Fg,
		Bg:     palette.Bg,
		Dim:    palette.Dim,
		SelFg:  palette.Bg,
		SelBg:  palette.Fg,
		Border: palette.Border,
	})
	md.WidthPct = 80

	m.fileModal = &md
}

func (m *Model) openKeybindsModal() {
	items := []modal.Item{
		{Name: "ctrl+h", Desc: "keybinds"},
		{Name: "ctrl+f", Desc: "file search"},
		{Name: "ctrl+s", Desc: "save editor (send diff) [experimental]"},
		{Name: "ctrl+shift+c", Desc: "copy selection"},
		{Name: "ctrl+shift+v", Desc: "paste"},
		{Name: "ctrl+c", Desc: "quit"},
		{Name: "esc", Desc: "cancel/blur"},
		{Name: "enter", Desc: "send message in input, newline on editor"},
		{Name: "shift+enter", Desc: "newline in input"},
		{Name: "tab", Desc: "indent"},
		{Name: "backspace", Desc: "delete backward"},
		{Name: "delete", Desc: "delete forward"},
		{Name: "up/down/left/right", Desc: "move cursor"},
		{Name: "shift+arrows", Desc: "extend selection"},
		{Name: "home/end/ctrl+a/ctrl+e", Desc: "line start/end"},
		{Name: "pgup/pgdown", Desc: "page scroll"},
		{Name: "shift+pgup/shift+pgdown", Desc: "extend selection by page"},
		{Name: "ctrl+home/ctrl+end", Desc: "file start/end"},
	}
	searchFn := func(query string) []modal.Item {
		if query == "" {
			return items
		}
		q := strings.ToLower(query)
		var filtered []modal.Item
		for _, item := range items {
			name := strings.ToLower(item.Name)
			desc := strings.ToLower(item.Desc)
			if strings.Contains(name, q) || strings.Contains(desc, q) {
				filtered = append(filtered, item)
			}
		}
		return filtered
	}
	md := modal.New(searchFn, "Keys: ", modal.Colors{
		Fg:     palette.Fg,
		Bg:     palette.Bg,
		Dim:    palette.Dim,
		SelFg:  palette.Bg,
		SelBg:  palette.Fg,
		Border: palette.Border,
	})
	md.WidthPct = 60
	m.keybindsModal = &md
}

func (m *Model) updateKeybindsModal(msg tea.Msg) (Model, tea.Cmd, bool) {
	if m.keybindsModal == nil {
		return *m, nil, false
	}
	action, cmd := m.keybindsModal.HandleMsg(msg)
	switch action.(type) {
	case modal.ActionClose:
		m.keybindsModal = nil
		return *m, nil, true
	case modal.ActionSelect:
		return *m, nil, true
	}
	if cmd != nil {
		return *m, cmd, true
	}
	switch msg.(type) {
	case tea.KeyPressMsg, tea.MouseMsg:
		return *m, nil, true
	}
	return *m, nil, false
}

func (m *Model) updateFileModal(msg tea.Msg) (Model, tea.Cmd, bool) {
	if m.fileModal == nil {
		return *m, nil, false
	}
	action, cmd := m.fileModal.HandleMsg(msg)
	switch a := action.(type) {
	case modal.ActionClose:
		m.fileModal = nil
		return *m, nil, true
	case modal.ActionSelect:
		m.fileModal = nil
		openCmd := m.openFile(a.Item.Name, 0)
		return *m, openCmd, true
	}
	if cmd != nil {
		return *m, cmd, true
	}
	switch msg.(type) {
	case tea.KeyPressMsg, tea.MouseMsg:
		return *m, nil, true
	}
	return *m, nil, false
}
