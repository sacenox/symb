// Package store provides a SQLite-backed cache for web fetch and search results.
package store

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	_ "modernc.org/sqlite" // register sqlite driver
)

const schema = `
CREATE TABLE IF NOT EXISTS fetch_cache (
	url       TEXT PRIMARY KEY,
	result    TEXT NOT NULL,
	created   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS search_cache (
	query    TEXT PRIMARY KEY,
	result   TEXT NOT NULL,
	created  INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_fetch_created ON fetch_cache(created);
CREATE INDEX IF NOT EXISTS idx_search_created ON search_cache(created);
`

// Cache is a SQLite-backed cache for web results.
type Cache struct {
	mu  sync.Mutex
	db  *sql.DB
	ttl time.Duration
}

// Open creates or opens a cache database at the given path.
// ttl controls how long entries remain fresh.
func Open(dbPath string, ttl time.Duration) (*Cache, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open cache db: %w", err)
	}

	// SQLite pragmas for performance.
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("pragma %q: %w", pragma, err)
		}
	}

	// Migrate: drop old search_cache with keywords column and recreate.
	// This is a cache, so losing data is acceptable.
	if hasColumn(db, "search_cache", "keywords") {
		db.Exec("DROP TABLE search_cache") //nolint:errcheck // best-effort migration
	}

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}

	c := &Cache{db: db, ttl: ttl}
	c.purgeStale()
	return c, nil
}

// Close closes the database.
func (c *Cache) Close() error {
	if c == nil {
		return nil
	}
	return c.db.Close()
}

// --- Fetch cache ---

// GetFetch returns a cached fetch result for the given URL, or "" if miss/stale.
// Safe to call on a nil receiver (returns miss).
func (c *Cache) GetFetch(url string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	cutoff := time.Now().Add(-c.ttl).Unix()
	var result string
	err := c.db.QueryRow(
		"SELECT result FROM fetch_cache WHERE url = ? AND created > ?",
		url, cutoff,
	).Scan(&result)
	if err != nil {
		return "", false
	}
	return result, true
}

// SetFetch stores a fetch result. No-op on nil receiver.
func (c *Cache) SetFetch(url, result string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	_, err := c.db.Exec(
		"INSERT OR REPLACE INTO fetch_cache (url, result, created) VALUES (?, ?, ?)",
		url, result, time.Now().Unix(),
	)
	if err != nil {
		log.Warn().Err(err).Str("url", url).Msg("failed to cache fetch result")
	}
}

// --- Search cache ---

// GetSearch returns a cached search result for the exact query, or "" if miss/stale.
// Safe to call on a nil receiver (returns miss).
func (c *Cache) GetSearch(query string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	normalized := normalizeQuery(query)
	cutoff := time.Now().Add(-c.ttl).Unix()
	var result string
	err := c.db.QueryRow(
		"SELECT result FROM search_cache WHERE query = ? AND created > ?",
		normalized, cutoff,
	).Scan(&result)
	if err != nil {
		return "", false
	}
	return result, true
}

// SearchCachedContent looks for a cached result whose text content contains
// enough of the query keywords. This searches the actual cached results, not
// the original queries — so if the answer already exists in any cached result,
// we return it instead of making a new API call.
func (c *Cache) SearchCachedContent(query string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	queryKw := tokenize(query)
	if len(queryKw) < 2 {
		return "", false
	}

	cutoff := time.Now().Add(-c.ttl).Unix()
	rows, err := c.db.Query(
		"SELECT result FROM search_cache WHERE created > ?",
		cutoff,
	)
	if err != nil {
		return "", false
	}
	defer rows.Close()

	var bestResult string
	var bestScore float64
	var bestHits int

	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			continue
		}
		resultLower := strings.ToLower(result)
		score, hits := contentOverlap(queryKw, resultLower)
		if score > bestScore {
			bestScore = score
			bestHits = hits
			bestResult = result
		}
	}

	// Require at least 75% of query keywords found in content AND at least 3 hits.
	if bestScore >= 0.75 && bestHits >= 3 {
		return bestResult, true
	}
	return "", false
}

// SetSearch stores a search result. No-op on nil receiver.
func (c *Cache) SetSearch(query, result string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	normalized := normalizeQuery(query)
	_, err := c.db.Exec(
		"INSERT OR REPLACE INTO search_cache (query, result, created) VALUES (?, ?, ?)",
		normalized, result, time.Now().Unix(),
	)
	if err != nil {
		log.Warn().Err(err).Str("query", query).Msg("failed to cache search result")
	}
}

// hasColumn checks if a table has a specific column.
func hasColumn(db *sql.DB, table, column string) bool {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table)) //nolint:gosec // table name is hardcoded by caller
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			continue
		}
		if name == column {
			return true
		}
	}
	return false
}

// --- Helpers ---

// purgeStale removes entries older than the TTL.
func (c *Cache) purgeStale() {
	cutoff := time.Now().Add(-c.ttl).Unix()
	for _, table := range []string{"fetch_cache", "search_cache"} {
		res, err := c.db.Exec(
			fmt.Sprintf("DELETE FROM %s WHERE created <= ?", table), //nolint:gosec // table name is hardcoded
			cutoff,
		)
		if err != nil {
			log.Warn().Err(err).Str("table", table).Msg("failed to purge stale cache")
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			log.Info().Int64("deleted", n).Str("table", table).Msg("purged stale cache entries")
		}
	}
}

// normalizeQuery lowercases and trims a query string.
func normalizeQuery(q string) string {
	return strings.ToLower(strings.TrimSpace(q))
}

// stopWords are common words filtered out during tokenization.
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "is": true, "are": true,
	"was": true, "were": true, "be": true, "been": true, "being": true,
	"have": true, "has": true, "had": true, "do": true, "does": true,
	"did": true, "will": true, "would": true, "could": true, "should": true,
	"may": true, "might": true, "shall": true, "can": true,
	"for": true, "and": true, "but": true, "or": true, "nor": true,
	"not": true, "so": true, "yet": true, "to": true, "of": true,
	"in": true, "on": true, "at": true, "by": true, "with": true,
	"from": true, "as": true, "into": true, "about": true, "between": true,
	"through": true, "during": true, "before": true, "after": true,
	"above": true, "below": true, "up": true, "down": true, "out": true,
	"off": true, "over": true, "under": true, "again": true, "then": true,
	"once": true, "here": true, "there": true, "when": true, "where": true,
	"why": true, "how": true, "what": true, "which": true, "who": true,
	"whom": true, "this": true, "that": true, "these": true, "those": true,
	"i": true, "me": true, "my": true, "we": true, "our": true,
	"you": true, "your": true, "he": true, "him": true, "his": true,
	"she": true, "her": true, "it": true, "its": true, "they": true,
	"them": true, "their": true,
}

// tokenize splits a query into lowercase keywords, filtering stop words and short tokens.
func tokenize(query string) []string {
	words := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	var out []string
	for _, w := range words {
		w = strings.Trim(w, ".,;:!?\"'()-[]{}") // strip punctuation
		if len(w) < 2 || stopWords[w] {
			continue
		}
		out = append(out, w)
	}
	return out
}

// contentOverlap checks how many of the query keywords appear anywhere in the
// lowercased result text. Returns fraction and count.
func contentOverlap(queryKw []string, resultLower string) (float64, int) {
	if len(queryKw) == 0 {
		return 0, 0
	}
	hits := 0
	for _, kw := range queryKw {
		if strings.Contains(resultLower, kw) {
			hits++
		}
	}
	return float64(hits) / float64(len(queryKw)), hits
}
