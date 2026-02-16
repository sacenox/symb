package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/xonecas/symb/internal/provider"
)

const (
	SQLiteBusyMaxRetries    = 10
	SQLiteBusyBackoffStepMs = 50
	SQLiteBusyMaxBackoff    = time.Second
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
	Role         string
	Content      string
	Reasoning    string
	ToolCalls    json.RawMessage // JSON array
	ToolCallID   string
	CreatedAt    time.Time
	InputTokens  int
	OutputTokens int
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

// SaveMessage persists a message synchronously.
func (c *Cache) SaveMessage(sessionID string, msg SessionMessage) {
	if err := c.SaveMessages(sessionID, []SessionMessage{msg}); err != nil {
		log.Warn().Err(err).Str("session", sessionID).Msg("failed to save message")
	}
}

// SaveMessages persists a batch of messages atomically.
func (c *Cache) SaveMessages(sessionID string, msgs []SessionMessage) error {
	if c == nil || len(msgs) == 0 {
		return nil
	}

	var err error
	for attempt := 0; attempt <= SQLiteBusyMaxRetries; attempt++ {
		err = c.saveMessagesOnce(sessionID, msgs)
		if err == nil {
			return nil
		}
		if !IsSQLiteBusy(err) || attempt == SQLiteBusyMaxRetries {
			return err
		}
		backoff := time.Duration((attempt+1)*SQLiteBusyBackoffStepMs) * time.Millisecond
		if backoff > SQLiteBusyMaxBackoff {
			backoff = SQLiteBusyMaxBackoff
		}
		time.Sleep(backoff)
	}
	return err
}

// SaveMessageSync persists a message synchronously and returns its DB row ID.
// Used for turn-start messages where we need the ID immediately.
func (c *Cache) SaveMessageSync(sessionID string, msg SessionMessage) (int64, error) {
	if c == nil {
		return 0, nil
	}

	var err error
	for attempt := 0; attempt <= SQLiteBusyMaxRetries; attempt++ {
		id, attemptErr := c.saveMessageSyncOnce(sessionID, msg)
		if attemptErr == nil {
			return id, nil
		}
		err = attemptErr
		if !IsSQLiteBusy(err) || attempt == SQLiteBusyMaxRetries {
			return 0, err
		}
		backoff := time.Duration((attempt+1)*SQLiteBusyBackoffStepMs) * time.Millisecond
		if backoff > SQLiteBusyMaxBackoff {
			backoff = SQLiteBusyMaxBackoff
		}
		time.Sleep(backoff)
	}
	return 0, err
}

func (c *Cache) saveMessagesOnce(sessionID string, msgs []SessionMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	tx, err := c.db.Begin()
	if err != nil {
		return err
	}

	for _, msg := range msgs {
		tc := msg.ToolCalls
		if tc == nil {
			tc = json.RawMessage("[]")
		}
		if _, err := tx.Exec(
			`INSERT INTO messages (session_id, role, content, reasoning, tool_calls, tool_call_id, created, input_tokens, output_tokens)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			sessionID, msg.Role, msg.Content, msg.Reasoning, string(tc), msg.ToolCallID, msg.CreatedAt.Unix(),
			msg.InputTokens, msg.OutputTokens,
		); err != nil {
			if rbErr := tx.Rollback(); rbErr != nil {
				log.Warn().Err(rbErr).Msg("failed to rollback message save")
			}
			return err
		}
	}

	if _, err := tx.Exec("UPDATE sessions SET updated = ? WHERE id = ?", time.Now().Unix(), sessionID); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			log.Warn().Err(rbErr).Msg("failed to rollback message save")
		}
		return err
	}

	if err := tx.Commit(); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			log.Warn().Err(rbErr).Msg("failed to rollback message save")
		}
		return err
	}
	return nil
}

func (c *Cache) saveMessageSyncOnce(sessionID string, msg SessionMessage) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	tx, err := c.db.Begin()
	if err != nil {
		return 0, err
	}

	tc := msg.ToolCalls
	if tc == nil {
		tc = json.RawMessage("[]")
	}

	res, err := tx.Exec(
		`INSERT INTO messages (session_id, role, content, reasoning, tool_calls, tool_call_id, created, input_tokens, output_tokens)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID, msg.Role, msg.Content, msg.Reasoning, string(tc), msg.ToolCallID, msg.CreatedAt.Unix(),
		msg.InputTokens, msg.OutputTokens,
	)
	if err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			log.Warn().Err(rbErr).Msg("failed to rollback message save")
		}
		return 0, err
	}

	if _, err := tx.Exec("UPDATE sessions SET updated = ? WHERE id = ?", time.Now().Unix(), sessionID); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			log.Warn().Err(rbErr).Msg("failed to rollback message save")
		}
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			log.Warn().Err(rbErr).Msg("failed to rollback message save")
		}
		return 0, err
	}

	return res.LastInsertId()
}

func IsSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "SQLITE_BUSY") || strings.Contains(msg, "database is locked")
}

// DeleteMessagesFrom removes all messages with id >= minID for a session.
func (c *Cache) DeleteMessagesFrom(sessionID string, minID int64) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	_, err := c.db.Exec(
		"DELETE FROM messages WHERE session_id = ? AND id >= ?",
		sessionID, minID,
	)
	return err
}

// LoadLastMessage returns the most recent message for a session, or nil if none.
func (c *Cache) LoadLastMessage(sessionID string) (*SessionMessage, error) {
	if c == nil {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	var m SessionMessage
	var tc string
	var created int64
	err := c.db.QueryRow(
		`SELECT role, content, reasoning, tool_calls, tool_call_id, created, input_tokens, output_tokens
		 FROM messages WHERE session_id = ? ORDER BY id DESC LIMIT 1`, sessionID,
	).Scan(&m.Role, &m.Content, &m.Reasoning, &tc, &m.ToolCallID, &created, &m.InputTokens, &m.OutputTokens)
	if err != nil {
		return nil, err
	}
	m.ToolCalls = json.RawMessage(tc)
	m.CreatedAt = time.Unix(created, 0)
	return &m, nil
}

// LoadMessages returns all messages for a session, ordered by ID.
func (c *Cache) LoadMessages(sessionID string) ([]SessionMessage, error) {
	if c == nil {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	rows, err := c.db.Query(
		`SELECT role, content, reasoning, tool_calls, tool_call_id, created, input_tokens, output_tokens
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
		if err := rows.Scan(&m.Role, &m.Content, &m.Reasoning, &tc, &m.ToolCallID, &created, &m.InputTokens, &m.OutputTokens); err != nil {
			continue
		}
		m.ToolCalls = json.RawMessage(tc)
		m.CreatedAt = time.Unix(created, 0)
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// SessionSummary holds info for listing sessions.
type SessionSummary struct {
	ID        string
	Timestamp time.Time
	Preview   string // first 50 chars of last user message
}

// ListSessions returns sessions ordered by most recent user message.
func (c *Cache) ListSessions() ([]SessionSummary, error) {
	if c == nil {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	rows, err := c.db.Query(`
		SELECT s.id, m.created, m.content
		FROM sessions s
		JOIN messages m ON m.session_id = s.id
		WHERE m.role = 'user'
		  AND m.id = (
		    SELECT MAX(m2.id) FROM messages m2
		    WHERE m2.session_id = s.id AND m2.role = 'user'
		  )
		ORDER BY m.created DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SessionSummary
	for rows.Next() {
		var s SessionSummary
		var ts int64
		if err := rows.Scan(&s.ID, &ts, &s.Preview); err != nil {
			continue
		}
		s.Timestamp = time.Unix(ts, 0)
		if len(s.Preview) > 50 {
			s.Preview = s.Preview[:50]
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// LatestSessionID returns the session with the most recent user message.
func (c *Cache) LatestSessionID() (string, error) {
	if c == nil {
		return "", fmt.Errorf("no cache")
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	var id string
	err := c.db.QueryRow(`
		SELECT s.id FROM sessions s
		JOIN messages m ON m.session_id = s.id
		WHERE m.role = 'user'
		ORDER BY m.created DESC
		LIMIT 1`).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("no sessions found")
	}
	return id, nil
}

// ToProviderMessages converts stored messages to provider messages.
func ToProviderMessages(msgs []SessionMessage) []provider.Message {
	out := make([]provider.Message, 0, len(msgs))
	for _, m := range msgs {
		pm := provider.Message{
			Role:       m.Role,
			Content:    m.Content,
			Reasoning:  m.Reasoning,
			ToolCallID: m.ToolCallID,
			CreatedAt:  m.CreatedAt,
		}
		if len(m.ToolCalls) > 0 {
			var tcs []provider.ToolCall
			if err := json.Unmarshal(m.ToolCalls, &tcs); err == nil {
				pm.ToolCalls = tcs
			}
		}
		out = append(out, pm)
	}
	return out
}

// SessionExists returns true if a session with the given ID exists.
func (c *Cache) SessionExists(id string) (bool, error) {
	if c == nil {
		return false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	var count int
	err := c.db.QueryRow("SELECT COUNT(*) FROM sessions WHERE id = ?", id).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
