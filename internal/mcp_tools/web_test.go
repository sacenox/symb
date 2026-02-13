package mcp_tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWebCache_GetSet(t *testing.T) {
	c := NewWebCache()

	// Miss on empty cache.
	if _, ok := c.get("key"); ok {
		t.Fatal("expected cache miss")
	}

	// Set and hit.
	c.set("key", "value")
	v, ok := c.get("key")
	if !ok || v != "value" {
		t.Fatalf("expected cache hit with 'value', got %q ok=%v", v, ok)
	}
}

func TestWebCache_Expiry(t *testing.T) {
	c := NewWebCache()
	c.set("key", "value")

	// Manually backdate the entry.
	c.mu.Lock()
	e := c.entries["key"]
	e.createdAt = time.Now().Add(-25 * time.Hour)
	c.entries["key"] = e
	c.mu.Unlock()

	if _, ok := c.get("key"); ok {
		t.Fatal("expected cache miss for stale entry")
	}
}

func TestExtractText(t *testing.T) {
	html := []byte(`<html><head><title>Hello</title><script>var x=1;</script><style>body{}</style></head>
<body><h1>Title</h1><p>Some <b>bold</b> text.</p><div>Another block</div></body></html>`)

	text := extractText(html)

	for _, want := range []string{"Title", "Some bold text.", "Another block"} {
		if !containsStr(text, want) {
			t.Errorf("expected text to contain %q, got:\n%s", want, text)
		}
	}

	// Should NOT contain script/style content.
	for _, unwanted := range []string{"var x=1", "body{}"} {
		if containsStr(text, unwanted) {
			t.Errorf("expected text to NOT contain %q, got:\n%s", unwanted, text)
		}
	}
}

func TestExtractText_PlainText(t *testing.T) {
	// No HTML at all — should pass through.
	text := extractText([]byte("just plain text"))
	if text != "just plain text" {
		t.Errorf("expected plain passthrough, got %q", text)
	}
}

func TestTruncate(t *testing.T) {
	s := "hello world"
	if got := truncate(s, 100); got != s {
		t.Errorf("should not truncate, got %q", got)
	}
	if got := truncate(s, 5); got != "hello\n\n[Truncated]" {
		t.Errorf("unexpected truncation, got %q", got)
	}
}

func TestCollapseWhitespace(t *testing.T) {
	input := "  line1  \n\n\n\n  line2  \n  \n  line3  "
	got := collapseWhitespace(input)
	want := "line1\n\nline2\n\nline3"
	if got != want {
		t.Errorf("collapseWhitespace:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestWebFetchHandler_HTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><body><p>Hello from test</p></body></html>`))
	}))
	defer srv.Close()

	cache := NewWebCache()
	handler := MakeWebFetchHandler(cache)

	args, _ := json.Marshal(WebFetchArgs{URL: srv.URL})
	result, err := handler(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", result.Content[0].Text)
	}
	if !containsStr(result.Content[0].Text, "Hello from test") {
		t.Errorf("expected 'Hello from test' in result, got %q", result.Content[0].Text)
	}

	// Second call should hit cache.
	result2, _ := handler(context.Background(), args)
	if result2.Content[0].Text != result.Content[0].Text {
		t.Error("expected identical cached result")
	}
}

func TestWebFetchHandler_PlainText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("plain content"))
	}))
	defer srv.Close()

	cache := NewWebCache()
	handler := MakeWebFetchHandler(cache)

	args, _ := json.Marshal(WebFetchArgs{URL: srv.URL})
	result, err := handler(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content[0].Text != "plain content" {
		t.Errorf("expected 'plain content', got %q", result.Content[0].Text)
	}
}

func TestWebFetchHandler_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	cache := NewWebCache()
	handler := MakeWebFetchHandler(cache)

	args, _ := json.Marshal(WebFetchArgs{URL: srv.URL})
	result, _ := handler(context.Background(), args)
	if !result.IsError {
		t.Fatal("expected error result for 404")
	}
}

func TestWebFetchHandler_MissingURL(t *testing.T) {
	cache := NewWebCache()
	handler := MakeWebFetchHandler(cache)

	args, _ := json.Marshal(WebFetchArgs{})
	result, _ := handler(context.Background(), args)
	if !result.IsError {
		t.Fatal("expected error for missing URL")
	}
}

func TestWebSearchHandler_MissingKey(t *testing.T) {
	cache := NewWebCache()
	handler := MakeWebSearchHandler(cache, "", "")

	args, _ := json.Marshal(WebSearchArgs{Query: "test"})
	result, _ := handler(context.Background(), args)
	if !result.IsError {
		t.Fatal("expected error for missing API key")
	}
	if !containsStr(result.Content[0].Text, "not configured") {
		t.Errorf("expected 'not configured' in error, got %q", result.Content[0].Text)
	}
}

func TestWebSearchHandler_MissingQuery(t *testing.T) {
	cache := NewWebCache()
	handler := MakeWebSearchHandler(cache, "fake-key", "")

	args, _ := json.Marshal(WebSearchArgs{})
	result, _ := handler(context.Background(), args)
	if !result.IsError {
		t.Fatal("expected error for missing query")
	}
}

func TestWebSearchHandler_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify auth header.
		if r.Header.Get("x-api-key") != "test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		// Verify request body.
		var req exaSearchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		resp := exaSearchResponse{
			Results: []exaResult{
				{Title: "Result 1", URL: "https://example.com/1", Text: "Some text"},
				{Title: "Result 2", URL: "https://example.com/2", Text: "More text", PublishedDate: "2025-01-01"},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	t.Run("integration", func(t *testing.T) {
		cache := NewWebCache()
		handler := MakeWebSearchHandler(cache, "test-key", srv.URL)

		args, _ := json.Marshal(WebSearchArgs{Query: "test query", NumResults: 2})
		result, err := handler(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.IsError {
			t.Fatalf("unexpected tool error: %s", result.Content[0].Text)
		}
		if !containsStr(result.Content[0].Text, "Result 1") {
			t.Errorf("expected 'Result 1' in output, got %q", result.Content[0].Text)
		}
		if !containsStr(result.Content[0].Text, "Published: 2025-01-01") {
			t.Errorf("expected published date in output, got %q", result.Content[0].Text)
		}

		// Second call should hit cache.
		result2, _ := handler(context.Background(), args)
		if result2.Content[0].Text != result.Content[0].Text {
			t.Error("expected identical cached result on second call")
		}
	})

	t.Run("bad_auth", func(t *testing.T) {
		cache := NewWebCache()
		handler := MakeWebSearchHandler(cache, "wrong-key", srv.URL)

		args, _ := json.Marshal(WebSearchArgs{Query: "test"})
		result, _ := handler(context.Background(), args)
		if !result.IsError {
			t.Fatal("expected error for bad auth")
		}
	})

	t.Run("formatSearchResults", func(t *testing.T) {
		results := []exaResult{
			{Title: "Go docs", URL: "https://go.dev", Text: "Go programming language"},
			{Title: "News", URL: "https://news.com", Text: "Latest", PublishedDate: "2025-06-01"},
		}
		out := formatSearchResults(results)
		if !containsStr(out, "Go docs") || !containsStr(out, "https://go.dev") {
			t.Errorf("missing expected content in:\n%s", out)
		}
		if !containsStr(out, "Published: 2025-06-01") {
			t.Errorf("missing published date in:\n%s", out)
		}
		if !containsStr(out, "Found 2 result(s)") {
			t.Errorf("missing result count in:\n%s", out)
		}
	})

	t.Run("formatSearchResults_empty", func(t *testing.T) {
		out := formatSearchResults(nil)
		if out != noSearchResults {
			t.Errorf("expected %q, got %q", noSearchResults, out)
		}
	})
}

func TestFormatSearchResults_NoResults(t *testing.T) {
	got := formatSearchResults(nil)
	if got != noSearchResults {
		t.Errorf("expected %q, got %q", noSearchResults, got)
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
