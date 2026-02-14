package editor

import tea "charm.land/bubbletea/v2"

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if m.focus {
			if moved := m.handleKeyPress(msg); moved {
				m.clampCursor()
				m.clampScroll()
				cmds = append(cmds, m.cursor.Blink())
			}
		}
	case tea.MouseClickMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg, tea.MouseWheelMsg:
		if m.focus {
			m.handleEditorMouse(msg)
		}
	}

	var cmd tea.Cmd
	m.cursor, cmd = m.cursor.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

// handleKeyPress dispatches a key event. Returns true if the cursor moved or content changed.
func (m *Model) handleKeyPress(msg tea.KeyPressMsg) bool {
	key := msg.Keystroke()

	if handled := m.handleShiftNav(key); handled {
		return true
	}
	if handled := m.handlePlainNav(key); handled {
		return true
	}
	if handled := m.handleEditKey(key); handled {
		return true
	}

	// Text insertion
	if !m.ReadOnly && msg.Text != "" {
		m.DeleteSelection()
		for _, r := range msg.Text {
			m.insertRune(r)
		}
		return true
	}
	return false
}

// handleShiftNav handles shift+arrow/pgup/pgdown/home/end for selection extension.
func (m *Model) handleShiftNav(key string) bool {
	switch key {
	case "shift+up":
		m.startOrExtendSelection()
		m.row--
		m.clampCursor()
		m.updateSelectionActive()
	case "shift+down":
		m.startOrExtendSelection()
		m.row++
		m.clampCursor()
		m.updateSelectionActive()
	case "shift+left":
		m.startOrExtendSelection()
		if m.col > 0 {
			m.col--
		} else if m.row > 0 {
			m.row--
			m.col = len(m.currentLine())
		}
		m.updateSelectionActive()
	case "shift+right":
		m.startOrExtendSelection()
		if m.col < len(m.currentLine()) {
			m.col++
		} else if m.row < len(m.lines)-1 {
			m.row++
			m.col = 0
		}
		m.updateSelectionActive()
	case "shift+home":
		m.startOrExtendSelection()
		m.col = 0
		m.updateSelectionActive()
	case "shift+end":
		m.startOrExtendSelection()
		m.col = len(m.currentLine())
		m.updateSelectionActive()
	case "shift+pgup":
		m.startOrExtendSelection()
		m.row -= m.height
		m.clampCursor()
		m.updateSelectionActive()
	case "shift+pgdown":
		m.startOrExtendSelection()
		m.row += m.height
		m.clampCursor()
		m.updateSelectionActive()
	default:
		return false
	}
	return true
}

// handlePlainNav handles plain arrow/home/end/pgup/pgdown navigation.
func (m *Model) handlePlainNav(key string) bool {
	switch key {
	case "up":
		m.ClearSelection()
		m.row--
		m.clampCursor()
	case "down":
		m.ClearSelection()
		m.row++
		m.clampCursor()
	case "left":
		m.ClearSelection()
		if m.col > 0 {
			m.col--
		} else if m.row > 0 {
			m.row--
			m.col = len(m.currentLine())
		}
	case "right":
		m.ClearSelection()
		if m.col < len(m.currentLine()) {
			m.col++
		} else if m.row < len(m.lines)-1 {
			m.row++
			m.col = 0
		}
	case "home", "ctrl+a":
		m.ClearSelection()
		m.col = 0
	case "end", "ctrl+e":
		m.ClearSelection()
		m.col = len(m.currentLine())
	case "pgup":
		m.ClearSelection()
		m.row -= m.height
		m.clampCursor()
	case "pgdown":
		m.ClearSelection()
		m.row += m.height
		m.clampCursor()
	case "ctrl+home":
		m.ClearSelection()
		m.row = 0
		m.col = 0
	case "ctrl+end":
		m.ClearSelection()
		m.row = len(m.lines) - 1
		m.col = len(m.currentLine())
	default:
		return false
	}
	return true
}

// handleEditKey handles backspace, delete, enter, tab.
func (m *Model) handleEditKey(key string) bool {
	switch key {
	case "backspace", "ctrl+h":
		if m.HasSelection() {
			m.DeleteSelection()
		} else {
			m.deleteBack()
		}
	case "delete", "ctrl+d":
		if m.HasSelection() {
			m.DeleteSelection()
		} else {
			m.deleteForward()
		}
	case "enter":
		m.DeleteSelection()
		m.insertNewline()
	case "tab":
		m.DeleteSelection()
		m.tabIndent()
	default:
		return false
	}
	return true
}

// handleEditorMouse dispatches mouse events for the editor.
func (m *Model) handleEditorMouse(msg tea.Msg) {
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			p := m.screenToPos(msg.X, msg.Y)
			m.dragging = true
			m.sel = &selection{anchor: p, active: p}
			m.row = p.row
			m.col = p.col
			m.clampCursor()
		}
	case tea.MouseMotionMsg:
		if m.dragging {
			p := m.screenToPos(msg.X, msg.Y)
			m.sel.active = p
			m.row = p.row
			m.col = p.col
			m.clampCursor()
		}
	case tea.MouseReleaseMsg:
		m.dragging = false
		if m.sel != nil && m.sel.empty() {
			m.ClearSelection()
		}
	case tea.MouseWheelMsg:
		if msg.Button == tea.MouseWheelUp {
			m.scroll -= 3
			m.clampScrollBounds()
		} else if msg.Button == tea.MouseWheelDown {
			m.scroll += 3
			m.clampScrollBounds()
		}
	}
}

// screenToPos converts screen-relative x,y to a buffer row,col.
// x,y are relative to the editor component origin.
func (m *Model) screenToPos(x, y int) pos {
	visRow := m.scroll + y
	if visRow < 0 {
		visRow = 0
	}
	bufRow, runeOffset := m.visualToBuffer(visRow)

	col := x - m.gutterWidth
	if col < 0 {
		col = 0
	}
	// runeOffset is in expanded-tab space; convert back to buffer col.
	// We need to find which buffer rune corresponds to runeOffset + col
	// in the expanded string.
	bufCol := m.expandedColToBufferCol(bufRow, runeOffset+col)
	return pos{row: bufRow, col: bufCol}
}
