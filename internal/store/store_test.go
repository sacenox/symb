package store

import (
	"path/filepath"
	"testing"
	"time"
)

func openTestCache(t *testing.T, ttl time.Duration) *Cache {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	c, err := Open(dbPath, ttl)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestFetchCache_SetGet(t *testing.T) {
	c := openTestCache(t, 24*time.Hour)

	// Miss on empty.
	if _, ok := c.GetFetch("https://example.com"); ok {
		t.Fatal("expected miss")
	}

	c.SetFetch("https://example.com", "page content")

	got, ok := c.GetFetch("https://example.com")
	if !ok {
		t.Fatal("expected hit")
	}
	if got != "page content" {
		t.Errorf("got %q, want %q", got, "page content")
	}
}

func TestFetchCache_Expiry(t *testing.T) {
	c := openTestCache(t, 1*time.Second)
	c.SetFetch("https://example.com", "content")

	// Backdate the entry.
	c.db.Exec("UPDATE fetch_cache SET created = ? WHERE url = ?",
		time.Now().Add(-2*time.Second).Unix(), "https://example.com")

	if _, ok := c.GetFetch("https://example.com"); ok {
		t.Fatal("expected stale miss")
	}
}

func TestSearchCache_SetGet(t *testing.T) {
	c := openTestCache(t, 24*time.Hour)

	if _, ok := c.GetSearch("golang context"); ok {
		t.Fatal("expected miss")
	}

	c.SetSearch("Golang Context", "results about context")

	// Exact match (case-insensitive via normalization).
	got, ok := c.GetSearch("golang context")
	if !ok {
		t.Fatal("expected hit")
	}
	if got != "results about context" {
		t.Errorf("got %q", got)
	}
}

func TestSearchCache_Expiry(t *testing.T) {
	c := openTestCache(t, 1*time.Second)
	c.SetSearch("golang context", "results")

	c.db.Exec("UPDATE search_cache SET created = ? WHERE query = ?",
		time.Now().Add(-2*time.Second).Unix(), "golang context")

	if _, ok := c.GetSearch("golang context"); ok {
		t.Fatal("expected stale miss")
	}
}

func TestSearchCachedContent(t *testing.T) {
	c := openTestCache(t, 24*time.Hour)

	// Seed cache with a result that contains content about Charm TUI tools.
	c.SetSearch("charm coding assistant tui", `Found 3 result(s):

--- 1. Crush - Charmbracelet coding assistant ---
URL: https://github.com/charmbracelet/crush
A terminal-based AI coding agent built with Bubbletea.

--- 2. Charm blog ---
URL: https://charm.sh/blog
Charm builds tools for the terminal including Bubbletea TUI framework.

--- 3. Z.AI docs ---
URL: https://z.ai/docs
AI agent for terminal workflows.
`)

	tests := []struct {
		name  string
		query string
		want  bool
	}{
		// These keywords appear in the cached result text:
		{"same topic different words", "charmbracelet terminal agent", true},
		{"synonym match", "charm bubbletea framework", true},
		{"partial content match", "coding agent terminal", true},
		// These don't appear enough in the cached content:
		{"unrelated", "python flask deployment", false},
		{"single keyword too few", "charm performance", false}, // only 1 hit < min 3
		{"below threshold", "react native mobile flutter", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := c.SearchCachedContent(tt.query)
			if ok != tt.want {
				t.Errorf("SearchCachedContent(%q) = _, %v; want %v", tt.query, ok, tt.want)
			}
		})
	}
}

func TestSearchCachedContent_Empty(t *testing.T) {
	c := openTestCache(t, 24*time.Hour)

	if _, ok := c.SearchCachedContent("anything here"); ok {
		t.Fatal("expected miss on empty cache")
	}
}

func TestSearchCachedContent_StaleIgnored(t *testing.T) {
	c := openTestCache(t, 1*time.Second)
	c.SetSearch("golang context package", "golang context cancellation patterns")

	c.db.Exec("UPDATE search_cache SET created = ? WHERE query = ?",
		time.Now().Add(-2*time.Second).Unix(), "golang context package")

	if _, ok := c.SearchCachedContent("golang context patterns"); ok {
		t.Fatal("expected stale entries to be ignored")
	}
}

func TestTokenize(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"Go context package", []string{"go", "context", "package"}},
		{"the best practices for Go", []string{"best", "practices", "go"}},
		{"a an the", nil},
		{"", nil},
		{"  React.js, hooks!  ", []string{"react.js", "hooks"}},
	}

	for _, tt := range tests {
		got := tokenize(tt.input)
		if !sliceEqual(got, tt.want) {
			t.Errorf("tokenize(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestContentOverlap(t *testing.T) {
	content := "charmbracelet crush is a terminal-based ai coding agent built with bubbletea"
	tests := []struct {
		query     []string
		wantScore float64
		wantHits  int
	}{
		{[]string{"charmbracelet", "terminal"}, 1.0, 2},
		{[]string{"charm", "terminal"}, 1.0, 2}, // "charm" is substring of "charmbracelet"
		{[]string{"python", "flask"}, 0.0, 0},
		{[]string{"terminal", "coding", "agent", "react"}, 0.75, 3},
	}

	for _, tt := range tests {
		score, hits := contentOverlap(tt.query, content)
		if diff := score - tt.wantScore; diff > 0.01 || diff < -0.01 {
			t.Errorf("contentOverlap(%v) score = %f, want %f", tt.query, score, tt.wantScore)
		}
		if hits != tt.wantHits {
			t.Errorf("contentOverlap(%v) hits = %d, want %d", tt.query, hits, tt.wantHits)
		}
	}
}

func TestPurgeStale(t *testing.T) {
	c := openTestCache(t, 1*time.Second)
	c.SetFetch("https://old.com", "old")
	c.SetSearch("old query", "old result")

	// Backdate both.
	past := time.Now().Add(-2 * time.Second).Unix()
	c.db.Exec("UPDATE fetch_cache SET created = ?", past)
	c.db.Exec("UPDATE search_cache SET created = ?", past)

	// Add fresh entries.
	c.SetFetch("https://new.com", "new")
	c.SetSearch("new query", "new result")

	c.purgeStale()

	if _, ok := c.GetFetch("https://old.com"); ok {
		t.Error("stale fetch should be purged")
	}
	if _, ok := c.GetFetch("https://new.com"); !ok {
		t.Error("fresh fetch should survive purge")
	}
	if _, ok := c.GetSearch("old query"); ok {
		t.Error("stale search should be purged")
	}
	if _, ok := c.GetSearch("new query"); !ok {
		t.Error("fresh search should survive purge")
	}
}

func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
