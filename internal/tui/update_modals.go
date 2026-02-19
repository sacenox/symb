package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/rs/zerolog/log"
	"github.com/xonecas/symb/internal/filesearch"
	"github.com/xonecas/symb/internal/provider"
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
	md := modal.New(searchFn, "File: ", modal.Colors{
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
		{Name: "@", Desc: "file search"},
		{Name: "ctrl+m", Desc: "switch model"},
		{Name: "ctrl+shift+c", Desc: "copy selection"},
		{Name: "ctrl+shift+v", Desc: "paste"},
		{Name: "ctrl+c", Desc: "quit"},
		{Name: "esc", Desc: "cancel/blur"},
		{Name: "enter", Desc: "send message"},
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
		m.atOffset = 0
		return *m, nil, true
	case modal.ActionSelect:
		m.fileModal = nil
		m.atOffset = 0
		m.agentInput.InsertText(a.Item.Name)
		m.agentInput.Focus()
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

func (m *Model) fetchModelsCmd() tea.Cmd {
	// If we have a cache, open the modal immediately with cached data and
	// kick off a background refresh at the same time.
	if len(m.cachedModels) > 0 {
		cached := m.cachedModels
		registry := m.registry
		providerOpts := m.providerOpts
		return tea.Batch(
			func() tea.Msg { return modelsFetchedMsg{models: cached} },
			func() tea.Msg {
				log.Info().Msg("fetchModelsCmd: background refresh of all providers")
				models := registry.ListAllModels(context.Background(), providerOpts)
				log.Info().Int("count", len(models)).Msg("fetchModelsCmd: background refresh done")
				return modelsFetchedMsg{models: models}
			},
		)
	}
	registry := m.registry
	providerOpts := m.providerOpts
	return func() tea.Msg {
		log.Info().Msg("fetchModelsCmd: fetching all providers")
		models := registry.ListAllModels(context.Background(), providerOpts)
		log.Info().Int("count", len(models)).Msg("fetchModelsCmd: all providers returned")
		return modelsFetchedMsg{models: models}
	}
}

func (m *Model) openModelsModal(models []provider.TaggedModel) {
	items := make([]modal.Item, len(models))
	for i, pm := range models {
		desc := pm.ProviderName
		if pm.Model.ParamSize != "" {
			desc += " · " + pm.Model.ParamSize
		}
		if pm.Model.QuantLevel != "" {
			desc += " · " + pm.Model.QuantLevel
		}
		items[i] = modal.Item{Name: pm.ProviderName + "/" + pm.Model.Name, Desc: desc}
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
	md := modal.New(searchFn, "Model: ", modal.Colors{
		Fg:     palette.Fg,
		Bg:     palette.Bg,
		Dim:    palette.Dim,
		SelFg:  palette.Bg,
		SelBg:  palette.Fg,
		Border: palette.Border,
	})
	md.WidthPct = 60
	m.modelsModal = &md
}

func (m *Model) updateModelsModal(msg tea.Msg) (Model, tea.Cmd, bool) {
	if m.modelsModal == nil {
		return *m, nil, false
	}
	action, cmd := m.modelsModal.HandleMsg(msg)
	switch a := action.(type) {
	case modal.ActionClose:
		m.modelsModal = nil
		return *m, nil, true
	case modal.ActionSelect:
		m.modelsModal = nil
		return *m, m.switchModelCmd(a.Item.Name), true
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

// switchModelCmd accepts a selection of the form "providerName/modelName" as
// produced by openModelsModal, or a bare model name (falls back to the current
// provider config name for backwards compatibility).
func (m *Model) switchModelCmd(selection string) tea.Cmd {
	registry := m.registry
	currentProviderConfigName := m.providerConfigName
	providerOpts := m.providerOpts
	oldProv := m.provider

	// Parse "providerName/modelName". Use SplitN so model names containing
	// additional slashes (e.g. "org/repo/model") are preserved intact.
	providerConfigName := currentProviderConfigName
	modelName := selection
	if parts := strings.SplitN(selection, "/", 2); len(parts) == 2 {
		providerConfigName = parts[0]
		modelName = parts[1]
	}

	return func() tea.Msg {
		if registry == nil {
			return modelSwitchedMsg{err: provider.ErrProviderNotFound}
		}
		log.Info().Str("provider", providerConfigName).Str("model", modelName).Msg("switchModelCmd")
		newProv, err := registry.Create(providerConfigName, modelName, providerOpts)
		if err != nil {
			return modelSwitchedMsg{err: err}
		}
		if oldProv != nil {
			oldProv.Close()
		}
		return modelSwitchedMsg{modelName: modelName, providerName: providerConfigName, prov: newProv}
	}
}

func (m *Model) openToolViewModal(title, content string) {
	tv := modal.NewToolView(title, content, modal.Colors{
		Fg:     palette.Fg,
		Bg:     palette.Bg,
		Dim:    palette.Dim,
		SelFg:  palette.Bg,
		SelBg:  palette.Fg,
		Border: palette.Border,
	})
	m.toolViewModal = &tv
}

func (m *Model) updateToolViewModal(msg tea.Msg) (Model, tea.Cmd, bool) {
	if m.toolViewModal == nil {
		return *m, nil, false
	}
	action, cmd := m.toolViewModal.HandleMsg(msg)
	switch action.(type) {
	case modal.ActionClose:
		m.toolViewModal = nil
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

func (m *Model) handleModelsFetched(msg modelsFetchedMsg) tea.Model {
	if msg.err != nil {
		log.Error().Err(msg.err).Msg("handleModelsFetched error")
		m.lastNetError = "Failed to list models: " + msg.err.Error()
		return m
	}
	if len(msg.models) == 0 {
		log.Warn().Msg("handleModelsFetched: no models")
		m.lastNetError = "No models available"
		return m
	}
	// Update cache whenever we receive a fresh (non-empty) list.
	m.cachedModels = msg.models
	// Don't clobber an already-open modal (background refresh case).
	if m.modelsModal != nil {
		return m
	}
	log.Info().Int("count", len(msg.models)).Msg("handleModelsFetched opening modal")
	m.openModelsModal(msg.models)
	return m
}

func (m *Model) handleModelSwitched(msg modelSwitchedMsg) tea.Model {
	if msg.err != nil {
		m.lastNetError = "Failed to switch model: " + msg.err.Error()
		return m
	}
	m.provider = msg.prov
	if m.sharedProvider != nil {
		prov := msg.prov
		m.sharedProvider.Store(&prov)
	}
	m.currentModelName = msg.modelName
	if msg.providerName != "" {
		m.providerConfigName = msg.providerName
	}
	return m
}
