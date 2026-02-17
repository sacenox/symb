package tui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"
	"github.com/rs/zerolog/log"
)

// demoteOldUndo finds the existing entryUndo in convEntries and removes it.
// The entrySeparator line above it is already present and stays.
// Called before appending a new undo entry so only the latest turn shows the undo control.
func (m *Model) demoteOldUndo() {
	for i := len(m.convEntries) - 1; i >= 0; i-- {
		if m.convEntries[i].kind == entryUndo {
			m.convEntries = append(m.convEntries[:i], m.convEntries[i+1:]...)
			return
		}
	}
}

// trimOldTurns drops the oldest display turns when we exceed maxDisplayTurns.
// Messages live in the DB — this only trims display entries to bound rendering cost.
func (m *Model) trimOldTurns() {
	for len(m.turnBoundaries) > maxDisplayTurns {
		cutConv := m.turnBoundaries[1].convIdx

		m.convEntries = m.convEntries[cutConv:]
		m.turnBoundaries = m.turnBoundaries[1:]

		for i := range m.turnBoundaries {
			m.turnBoundaries[i].convIdx -= cutConv
		}
	}

	m.frameLines = nil
	lines := m.wrappedConvLines()
	convH := m.layout.conv.Dy()
	maxScroll := len(lines) - convH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.scrollOffset > maxScroll {
		m.scrollOffset = maxScroll
	}
}

// handleUndo reverts the most recent turn: restores files, truncates history
// and convEntries, and cleans up the database.
func (m *Model) handleUndo() (Model, tea.Cmd) {
	if m.streaming || len(m.turnBoundaries) == 0 {
		return *m, nil
	}
	if m.undoInFlight {
		return *m, nil
	}

	tb := m.turnBoundaries[len(m.turnBoundaries)-1]
	m.turnBoundaries = m.turnBoundaries[:len(m.turnBoundaries)-1]

	// Restore token totals to the snapshot at turn start.
	m.totalInputTokens = tb.inputTokens
	m.totalOutputTokens = tb.outputTokens
	m.turnInputTokens = 0
	m.turnOutputTokens = 0

	// 1. Truncate display entries.
	m.convEntries = m.convEntries[:tb.convIdx]

	// 6. Promote previous turn's demoted separator back to undo, if any.
	if len(m.turnBoundaries) > 0 {
		for i := len(m.convEntries) - 1; i >= 0; i-- {
			if m.convEntries[i].kind == entrySeparator {
				// Replace separator with separator+undo pair.
				entries := m.makeUndoEntry(m.convEntries[i].full)
				// Replace the separator entry in-place and insert undo after it.
				m.convEntries[i] = entries[0]
				m.convEntries = append(m.convEntries[:i+1], append([]convEntry{entries[1]}, m.convEntries[i+1:]...)...)
				break
			}
		}
	}

	// 7. Reset streaming state.
	m.streaming = false
	m.streamEntryStart = -1
	m.streamingReasoning = ""
	m.streamingContent = ""

	// 8. Scroll to bottom.
	m.scrollOffset = 0

	m.undoInFlight = true
	cmd := m.undoSideEffectsCmd(tb.dbMsgID)
	return *m, cmd
}

func (m *Model) undoSideEffectsCmd(dbMsgID int64) tea.Cmd {
	tracker := m.deltaTracker
	store := m.store
	fileTracker := m.fileTracker
	tsIndex := m.tsIndex
	sessionID := m.sessionID
	return func() tea.Msg {
		var undoErr error
		var restoredFiles []string
		if tracker != nil && dbMsgID > 0 {
			restoredFiles, undoErr = tracker.Undo(sessionID, dbMsgID)
			tracker.DeleteTurn(sessionID, dbMsgID)
		}
		if store != nil && dbMsgID > 0 {
			if err := store.DeleteMessagesFrom(sessionID, dbMsgID); err != nil {
				log.Warn().Err(err).Msg("undo: failed to delete messages")
			}
		}
		if fileTracker != nil {
			fileTracker.Reset()
		}
		if tsIndex != nil {
			for _, f := range restoredFiles {
				tsIndex.UpdateFile(f)
			}
		}
		return undoResultMsg{err: undoErr}
	}
}

func (m Model) handleUndoResult(msg undoResultMsg) Model {
	m.undoInFlight = false
	if msg.err == nil || errors.Is(msg.err, context.Canceled) {
		return m
	}
	m.appendText("", m.styles.Error.Render("undo file restore failed: "+msg.err.Error()), "")
	return m
}
