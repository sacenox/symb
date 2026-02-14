package store

import (
	"encoding/json"
	"time"

	"github.com/rs/zerolog/log"
)

// Session represents a conversation session.
type Session struct {
	ID      string
	Title   string
	Created time.Time
	Updated time.Time
}

// SessionMessage is a persisted chat message.
type SessionMessage struct {
	Role       string
	Content    string
	Reasoning  string
	ToolCalls  json.RawMessage // JSON array
	ToolCallID string
	CreatedAt  time.Time
}

// CreateSession inserts a new session and returns its ID.
func (c *Cache) CreateSession(id string) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now().Unix()
	_, err := c.db.Exec(
		"INSERT INTO sessions (id, title, created, updated) VALUES (?, '', ?, ?)",
		id, now, now,
	)
	if err != nil {
		log.Warn().Err(err).Str("id", id).Msg("failed to create session")
	}
	return err
}

// SaveMessage queues a message for async persistence. Non-blocking.
func (c *Cache) SaveMessage(sessionID string, msg SessionMessage) {
	if c == nil {
		return
	}
	select {
	case c.saveCh <- saveReq{sessionID: sessionID, msg: msg}:
	default:
		log.Warn().Str("session", sessionID).Msg("save channel full, dropping message")
	}
}

// saveLoop drains saveCh and writes messages to the DB.
func (c *Cache) saveLoop() {
	defer close(c.done)
	for req := range c.saveCh {
		c.writeMessage(req.sessionID, req.msg)
	}
}

// writeMessage performs the actual DB insert for a message.
func (c *Cache) writeMessage(sessionID string, msg SessionMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()

	tc := msg.ToolCalls
	if tc == nil {
		tc = json.RawMessage("[]")
	}

	_, err := c.db.Exec(
		`INSERT INTO messages (session_id, role, content, reasoning, tool_calls, tool_call_id, created)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sessionID, msg.Role, msg.Content, msg.Reasoning, string(tc), msg.ToolCallID, msg.CreatedAt.Unix(),
	)
	if err != nil {
		log.Warn().Err(err).Str("session", sessionID).Msg("failed to save message")
	}

	// Touch session updated time.
	c.db.Exec("UPDATE sessions SET updated = ? WHERE id = ?", time.Now().Unix(), sessionID) //nolint:errcheck
}

// LoadMessages returns all messages for a session, ordered by ID.
func (c *Cache) LoadMessages(sessionID string) ([]SessionMessage, error) {
	if c == nil {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	rows, err := c.db.Query(
		`SELECT role, content, reasoning, tool_calls, tool_call_id, created
		 FROM messages WHERE session_id = ? ORDER BY id`, sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []SessionMessage
	for rows.Next() {
		var m SessionMessage
		var tc string
		var created int64
		if err := rows.Scan(&m.Role, &m.Content, &m.Reasoning, &tc, &m.ToolCallID, &created); err != nil {
			continue
		}
		m.ToolCalls = json.RawMessage(tc)
		m.CreatedAt = time.Unix(created, 0)
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}
