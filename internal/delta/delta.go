// Package delta tracks filesystem changes made by tool calls so they can be
// reversed on undo. Deltas are persisted to SQLite and keyed by (session, turn).
package delta

import (
	"database/sql"
	"os"
	"sync"

	"github.com/rs/zerolog/log"
)

// Tracker records and replays filesystem deltas.
type Tracker struct {
	mu        sync.Mutex
	db        *sql.DB
	sessionID string
	turnID    int64 // current turn; 0 = no active turn
}

// New creates a Tracker that writes to the given database.
func New(db *sql.DB) *Tracker {
	return &Tracker{db: db}
}

// SetSession sets the active session ID.
func (t *Tracker) SetSession(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sessionID = id
}

// BeginTurn sets the current turn ID. All subsequent Record* calls
// are associated with this turn until the next BeginTurn.
func (t *Tracker) BeginTurn(turnID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.turnID = turnID
}

// TurnID returns the current turn ID.
func (t *Tracker) TurnID() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.turnID
}

// RecordModify stores the original content of a file before it is modified.
// Only the first snapshot per file per turn is kept (subsequent edits to the
// same file in the same turn are no-ops — we already have the original).
func (t *Tracker) RecordModify(filePath string, oldContent []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.turnID == 0 || t.sessionID == "" {
		return
	}
	// Check if we already have a delta for this file in this turn.
	var exists bool
	err := t.db.QueryRow(
		`SELECT 1 FROM file_deltas WHERE session_id = ? AND turn_id = ? AND file_path = ? LIMIT 1`,
		t.sessionID, t.turnID, filePath,
	).Scan(&exists)
	if err == nil && exists {
		return // already recorded
	}
	_, err = t.db.Exec(
		`INSERT INTO file_deltas (session_id, turn_id, file_path, op, old_content, created)
		 VALUES (?, ?, ?, 'modify', ?, strftime('%s','now'))`,
		t.sessionID, t.turnID, filePath, oldContent,
	)
	if err != nil {
		log.Warn().Err(err).Str("file", filePath).Msg("failed to record modify delta")
	}
}

// RecordCreate records that a file was created (old_content is NULL).
func (t *Tracker) RecordCreate(filePath string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.turnID == 0 || t.sessionID == "" {
		return
	}
	_, err := t.db.Exec(
		`INSERT INTO file_deltas (session_id, turn_id, file_path, op, old_content, created)
		 VALUES (?, ?, ?, 'create', NULL, strftime('%s','now'))`,
		t.sessionID, t.turnID, filePath,
	)
	if err != nil {
		log.Warn().Err(err).Str("file", filePath).Msg("failed to record create delta")
	}
}

// Undo reverses all file changes for the given turn, in reverse order.
// Modify ops restore old content; create ops delete the file.
// Returns the list of affected absolute file paths and any error.
func (t *Tracker) Undo(sessionID string, turnID int64) ([]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	rows, err := t.db.Query(
		`SELECT file_path, op, old_content FROM file_deltas
		 WHERE session_id = ? AND turn_id = ?
		 ORDER BY id DESC`,
		sessionID, turnID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var affected []string
	for rows.Next() {
		var filePath, op string
		var oldContent []byte
		if err := rows.Scan(&filePath, &op, &oldContent); err != nil {
			log.Warn().Err(err).Msg("failed to scan delta row")
			continue
		}
		affected = append(affected, filePath)
		switch op {
		case "modify":
			if err := os.WriteFile(filePath, oldContent, 0600); err != nil {
				log.Warn().Err(err).Str("file", filePath).Msg("undo: failed to restore file")
			}
		case "create":
			if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
				log.Warn().Err(err).Str("file", filePath).Msg("undo: failed to remove created file")
			}
		}
	}
	return affected, rows.Err()
}

// DeleteTurn removes all delta records for a turn.
func (t *Tracker) DeleteTurn(sessionID string, turnID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, err := t.db.Exec(
		`DELETE FROM file_deltas WHERE session_id = ? AND turn_id = ?`,
		sessionID, turnID,
	)
	if err != nil {
		log.Warn().Err(err).Int64("turn", turnID).Msg("failed to delete turn deltas")
	}
}
