package mcptools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/xonecas/symb/internal/mcp"
	"github.com/xonecas/symb/internal/store"
	"golang.org/x/net/html"
)

// noSearchResults is the message returned when no search results are found.
const noSearchResults = "No results found."

// --- WebFetch ---

// WebFetchArgs represents arguments for the WebFetch tool.
type WebFetchArgs struct {
	URL      string `json:"url"`
	MaxChars int    `json:"max_chars,omitempty"`
}

// NewWebFetchTool creates the WebFetch tool definition.
func NewWebFetchTool() mcp.Tool {
	return mcp.Tool{
		Name:        "WebFetch",
		Description: "Fetch a URL and return its content as cleaned text (HTML tags, scripts, and styles stripped). Results are cached.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"url":       {"type": "string", "description": "The URL to fetch."},
				"max_chars": {"type": "integer", "description": "Maximum characters to return. Default: 10000"}
			},
			"required": ["url"]
		}`),
	}
}

// MakeWebFetchHandler creates a handler for the WebFetch tool.
func MakeWebFetchHandler(cache *store.Cache) mcp.ToolHandler {
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

		// Check cache (keyed by URL).
		if cached, ok := cache.GetFetch(args.URL); ok {
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

		cache.SetFetch(args.URL, text)
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
	return mcp.Tool{
		Name:        "WebSearch",
		Description: "Search the web using Exa AI. Use this to look up documentation, APIs, libraries, or current information. Results are cached.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query":           {"type": "string", "description": "Search query."},
				"num_results":     {"type": "integer", "description": "Number of results to return. Default: 5"},
				"type":            {"type": "string", "description": "Search type: \"auto\" (default), \"fast\", or \"deep\".", "enum": ["auto", "fast", "deep"]},
				"include_domains": {"type": "array", "items": {"type": "string"}, "description": "Only include results from these domains."}
			},
			"required": ["query"]
		}`),
	}
}

const exaDefaultEndpoint = "https://api.exa.ai/search"

// MakeWebSearchHandler creates a handler for the WebSearch tool.
// endpoint is the Exa API URL; pass "" to use the default (https://api.exa.ai/search).
func MakeWebSearchHandler(cache *store.Cache, apiKey, endpoint string) mcp.ToolHandler {
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

		// Build exact cache key including params so different num_results/type
		// don't return wrong cached results.
		exactKey := fmt.Sprintf("%s|n=%d|t=%s|d=%s",
			args.Query, args.NumResults, args.Type,
			strings.Join(args.IncludeDomains, ","))

		// Check exact cache hit first (query + params).
		if cached, ok := cache.GetSearch(exactKey); ok {
			log.Debug().Str("query", args.Query).Msg("WebSearch exact cache hit")
			return toolText(cached), nil
		}

		// Search cached result content for query keywords — avoids API call
		// if the answer already exists in a previously cached result.
		if cached, ok := cache.SearchCachedContent(args.Query); ok {
			log.Debug().Str("query", args.Query).Msg("WebSearch content cache hit")
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
		cache.SetSearch(exactKey, result)
		return toolText(result), nil
	}
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

// isSkipTag returns true for tags whose content should be suppressed.
func isSkipTag(tag string) bool {
	return tag == "script" || tag == "style" || tag == "noscript"
}

// extractText parses HTML and returns visible text content.
// Strips script, style, and noscript elements.
func extractText(data []byte) string {
	tokenizer := html.NewTokenizer(bytes.NewReader(data))
	var b strings.Builder
	skip := 0

	for {
		tt := tokenizer.Next()
		if tt == html.ErrorToken {
			return collapseWhitespace(b.String())
		}
		tn, _ := tokenizer.TagName()
		tag := string(tn)

		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			if isSkipTag(tag) {
				skip++
			}
			if isBlockElement(tag) && b.Len() > 0 {
				b.WriteByte('\n')
			}
		case html.EndTagToken:
			if isSkipTag(tag) && skip > 0 {
				skip--
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
