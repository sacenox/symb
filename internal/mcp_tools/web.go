package mcp_tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/xonecas/symb/internal/mcp"
	"golang.org/x/net/html"
)

// cacheTTL is how long cached entries remain fresh.
const cacheTTL = 24 * time.Hour

// cacheEntry stores a cached result with its timestamp.
type cacheEntry struct {
	result    string
	createdAt time.Time
}

// noSearchResults is the message returned when no search results are found.
const noSearchResults = "No results found."

// WebCache is a shared in-memory cache for WebFetch and WebSearch results.
type WebCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
}

func newWebCache() *WebCache {
	return &WebCache{entries: make(map[string]cacheEntry)}
}

// get returns the cached value and true if it exists and is fresh.
func (c *WebCache) get(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.entries[key]
	if !ok {
		return "", false
	}
	if time.Since(entry.createdAt) > cacheTTL {
		return "", false
	}
	return entry.result, true
}

// set stores a value in the cache.
func (c *WebCache) set(key, result string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[key] = cacheEntry{result: result, createdAt: time.Now()}
}

// --- WebFetch ---

// WebFetchArgs represents arguments for the WebFetch tool.
type WebFetchArgs struct {
	URL      string `json:"url"`
	MaxChars int    `json:"max_chars,omitempty"`
}

// NewWebFetchTool creates the WebFetch tool definition.
func NewWebFetchTool() mcp.Tool {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"url": map[string]interface{}{
				"type":        "string",
				"description": "The URL to fetch.",
			},
			"max_chars": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum characters to return. Default: 10000",
			},
		},
		"required": []string{"url"},
	}

	schemaJSON, _ := json.Marshal(schema)

	return mcp.Tool{
		Name:        "WebFetch",
		Description: "Fetch a URL and return its content as cleaned text (HTML tags, scripts, and styles stripped). Cached for 24 hours.",
		InputSchema: schemaJSON,
	}
}

// MakeWebFetchHandler creates a handler for the WebFetch tool.
func MakeWebFetchHandler(cache *WebCache) mcp.ToolHandler {
	client := &http.Client{Timeout: 15 * time.Second}

	return func(ctx context.Context, arguments json.RawMessage) (*mcp.ToolResult, error) {
		var args WebFetchArgs
		if err := json.Unmarshal(arguments, &args); err != nil {
			return toolError("Invalid arguments: %v", err), nil
		}

		if args.URL == "" {
			return toolError("url is required"), nil
		}
		if args.MaxChars <= 0 {
			args.MaxChars = 10000
		}

		// Check cache (keyed by raw URL).
		if cached, ok := cache.get(args.URL); ok {
			log.Debug().Str("url", args.URL).Msg("WebFetch cache hit")
			return toolText(truncate(cached, args.MaxChars)), nil
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, args.URL, nil)
		if err != nil {
			return toolError("Bad URL: %v", err), nil
		}
		req.Header.Set("User-Agent", "Symb/0.1")
		req.Header.Set("Accept", "text/html, text/plain;q=0.9, */*;q=0.5")

		resp, err := client.Do(req)
		if err != nil {
			return toolError("Fetch failed: %v", err), nil
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			return toolError("HTTP %d: %s", resp.StatusCode, resp.Status), nil
		}

		// Cap read at 1MB to avoid blowing up memory.
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return toolError("Read failed: %v", err), nil
		}

		contentType := resp.Header.Get("Content-Type")
		var text string
		if strings.Contains(contentType, "text/html") {
			text = extractText(body)
		} else {
			text = string(body)
		}

		cache.set(args.URL, text)
		return toolText(truncate(text, args.MaxChars)), nil
	}
}

// --- WebSearch ---

// WebSearchArgs represents arguments for the WebSearch tool.
type WebSearchArgs struct {
	Query          string   `json:"query"`
	NumResults     int      `json:"num_results,omitempty"`
	Type           string   `json:"type,omitempty"`
	IncludeDomains []string `json:"include_domains,omitempty"`
}

// exaSearchRequest is the request body for POST https://api.exa.ai/search.
type exaSearchRequest struct {
	Query          string            `json:"query"`
	Type           string            `json:"type"`
	NumResults     int               `json:"numResults"`
	Contents       exaSearchContents `json:"contents"`
	IncludeDomains []string          `json:"includeDomains,omitempty"`
}

type exaSearchContents struct {
	Text exaTextOptions `json:"text"`
}

type exaTextOptions struct {
	MaxCharacters int `json:"maxCharacters"`
}

// exaSearchResponse is the response from Exa's /search endpoint.
type exaSearchResponse struct {
	Results []exaResult `json:"results"`
}

type exaResult struct {
	Title         string `json:"title"`
	URL           string `json:"url"`
	Text          string `json:"text"`
	PublishedDate string `json:"publishedDate,omitempty"`
}

// NewWebSearchTool creates the WebSearch tool definition.
func NewWebSearchTool() mcp.Tool {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Search query.",
			},
			"num_results": map[string]interface{}{
				"type":        "integer",
				"description": "Number of results to return. Default: 5",
			},
			"type": map[string]interface{}{
				"type":        "string",
				"description": "Search type: \"auto\" (default), \"fast\", or \"deep\".",
				"enum":        []string{"auto", "fast", "deep"},
			},
			"include_domains": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Only include results from these domains.",
			},
		},
		"required": []string{"query"},
	}

	schemaJSON, _ := json.Marshal(schema)

	return mcp.Tool{
		Name:        "WebSearch",
		Description: "Search the web using Exa AI. Use this to look up documentation, APIs, libraries, or current information. Cached for 24 hours.",
		InputSchema: schemaJSON,
	}
}

const exaDefaultEndpoint = "https://api.exa.ai/search"

// MakeWebSearchHandler creates a handler for the WebSearch tool.
// endpoint is the Exa API URL; pass "" to use the default (https://api.exa.ai/search).
func MakeWebSearchHandler(cache *WebCache, apiKey, endpoint string) mcp.ToolHandler {
	if endpoint == "" {
		endpoint = exaDefaultEndpoint
	}
	client := &http.Client{Timeout: 15 * time.Second}

	return func(ctx context.Context, arguments json.RawMessage) (*mcp.ToolResult, error) {
		var args WebSearchArgs
		if err := json.Unmarshal(arguments, &args); err != nil {
			return toolError("Invalid arguments: %v", err), nil
		}

		if args.Query == "" {
			return toolError("query is required"), nil
		}
		if apiKey == "" {
			return toolError("Exa AI API key not configured in credentials.json (providers.exa_ai.api_key)"), nil
		}
		if args.NumResults <= 0 {
			args.NumResults = 5
		}
		if args.Type == "" {
			args.Type = "auto"
		}

		// Check cache (keyed by normalized query + params).
		cacheKey := fmt.Sprintf("search:%s:n=%d:t=%s:d=%s",
			strings.ToLower(strings.TrimSpace(args.Query)),
			args.NumResults, args.Type, strings.Join(args.IncludeDomains, ","))
		if cached, ok := cache.get(cacheKey); ok {
			log.Debug().Str("query", args.Query).Msg("WebSearch cache hit")
			return toolText(cached), nil
		}

		body := exaSearchRequest{
			Query:      args.Query,
			Type:       args.Type,
			NumResults: args.NumResults,
			Contents: exaSearchContents{
				Text: exaTextOptions{MaxCharacters: 2000},
			},
			IncludeDomains: args.IncludeDomains,
		}

		bodyJSON, err := json.Marshal(body)
		if err != nil {
			return toolError("Marshal failed: %v", err), nil
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyJSON))
		if err != nil {
			return toolError("Request failed: %v", err), nil
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", apiKey)

		resp, err := client.Do(req)
		if err != nil {
			return toolError("Search failed: %v", err), nil
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return toolError("Read response failed: %v", err), nil
		}

		if resp.StatusCode >= 400 {
			return toolError("Exa API error %d: %s", resp.StatusCode, string(respBody)), nil
		}

		var exaResp exaSearchResponse
		if err := json.Unmarshal(respBody, &exaResp); err != nil {
			return toolError("Parse response failed: %v", err), nil
		}

		result := formatSearchResults(exaResp.Results)
		cache.set(cacheKey, result)
		return toolText(result), nil
	}
}

// NewWebCache creates a shared cache for web tools.
func NewWebCache() *WebCache {
	return newWebCache()
}

// --- Helpers ---

// formatSearchResults formats Exa results into readable text.
func formatSearchResults(results []exaResult) string {
	if len(results) == 0 {
		return noSearchResults
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Found %d result(s):\n", len(results)))
	for i, r := range results {
		b.WriteString(fmt.Sprintf("\n--- %d. %s ---\n", i+1, r.Title))
		b.WriteString(fmt.Sprintf("URL: %s\n", r.URL))
		if r.PublishedDate != "" {
			b.WriteString(fmt.Sprintf("Published: %s\n", r.PublishedDate))
		}
		if r.Text != "" {
			b.WriteString(r.Text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// extractText parses HTML and returns visible text content.
// Strips script, style, and noscript elements.
func extractText(data []byte) string {
	tokenizer := html.NewTokenizer(bytes.NewReader(data))
	var b strings.Builder
	skip := 0 // nesting depth inside skip elements

	for {
		tt := tokenizer.Next()
		switch tt {
		case html.ErrorToken:
			return collapseWhitespace(b.String())

		case html.StartTagToken, html.SelfClosingTagToken:
			tn, _ := tokenizer.TagName()
			tag := string(tn)
			if tag == "script" || tag == "style" || tag == "noscript" {
				skip++
			}
			// Insert whitespace for block elements.
			if isBlockElement(tag) && b.Len() > 0 {
				b.WriteByte('\n')
			}

		case html.EndTagToken:
			tn, _ := tokenizer.TagName()
			tag := string(tn)
			if tag == "script" || tag == "style" || tag == "noscript" {
				if skip > 0 {
					skip--
				}
			}

		case html.TextToken:
			if skip == 0 {
				b.Write(tokenizer.Text())
			}
		}
	}
}

// isBlockElement returns true for HTML elements that typically start a new line.
func isBlockElement(tag string) bool {
	switch tag {
	case "p", "div", "br", "h1", "h2", "h3", "h4", "h5", "h6",
		"li", "tr", "td", "th", "blockquote", "pre", "hr",
		"header", "footer", "section", "article", "nav", "main":
		return true
	}
	return false
}

// collapseWhitespace trims each line and collapses multiple blank lines.
func collapseWhitespace(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	blanks := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			blanks++
			if blanks <= 1 {
				out = append(out, "")
			}
			continue
		}
		blanks = 0
		out = append(out, trimmed)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// truncate cuts a string to maxChars (rune-safe).
func truncate(s string, maxChars int) string {
	runes := []rune(s)
	if len(runes) <= maxChars {
		return s
	}
	return string(runes[:maxChars]) + "\n\n[Truncated]"
}

// toolError returns an error ToolResult.
func toolError(format string, args ...interface{}) *mcp.ToolResult {
	return &mcp.ToolResult{
		Content: []mcp.ContentBlock{{Type: "text", Text: fmt.Sprintf(format, args...)}},
		IsError: true,
	}
}

// toolText returns a text ToolResult.
func toolText(text string) *mcp.ToolResult {
	return &mcp.ToolResult{
		Content: []mcp.ContentBlock{{Type: "text", Text: text}},
		IsError: false,
	}
}
