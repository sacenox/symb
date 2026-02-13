# Web/Search Tools Feature

## Current Architecture

- **Credentials**: `~/.config/symb/credentials.json` with `providers.<name>.api_key` structure. Already supports arbitrary provider names — `exa_ai` fits naturally.
- **Tools**: Defined as `mcp.Tool` (name + description + JSON schema), registered on the `mcp.Proxy` via `RegisterTool(tool, handler)`. Handlers are `func(ctx, json.RawMessage) (*ToolResult, error)`.
- **Prompts**: 4 model-specific markdown files (`anthropic.md`, `gemini.md`, `qwen.md`, `gpt.md`), embedded via `//go:embed`. Each documents the available tools.
- **Registration**: All in `cmd/symb/main.go` — create tool definition + handler, register on proxy.

## Exa AI API

- **Auth**: `x-api-key: <key>` header
- **Key endpoint**: `POST https://api.exa.ai/search` — supports `type: "auto"`, inline `contents` extraction (text, highlights, summary), domain filtering, date filtering
- **Response**: Returns `results[]` with `url`, `title`, `text`, `publishedDate`, etc.
- Free tier: $10 credits, ~2000 searches

## Design: Two New Tools

### 1. `WebFetch` — HTTP fetch with HTML cleaning

- **Purpose**: Fetch a URL and return cleaned text content (strip HTML tags, scripts, styles — preserve readable text)
- **Args**: `url` (required), `max_chars` (optional, default 10000)
- **Implementation**: Go's `net/http` GET, then HTML-to-text extraction (use `golang.org/x/net/html` tokenizer to strip tags)
- **Returns**: Cleaned text content, truncated to `max_chars`

### 2. `WebSearch` — Exa AI search

- **Purpose**: Search the web for information, documentation, etc.
- **Args**: `query` (required), `num_results` (optional, default 5), `type` (optional: "auto"/"fast"/"deep"), `include_domains` (optional), `include_text` (optional)
- **Implementation**: POST to `https://api.exa.ai/search` with `contents.text.maxCharacters` to keep tokens reasonable
- **Returns**: Formatted results (title, URL, snippet/text)
- **Requires**: Exa API key from `credentials.json` under `providers.exa_ai.api_key`

### New file: `internal/mcp_tools/web.go`

Both tools in one file. The Exa client is simple enough to not warrant a separate package — just HTTP POST with JSON.

### Search cache

In-memory cache shared by both `WebSearch` and `WebFetch`. Keyed by normalized string (lowercased + trimmed query for search, raw URL for fetch). Each entry stores the result and a timestamp. Entries older than 24 hours are considered stale and re-fetched. Simple `map` + `sync.RWMutex`, no persistence — cache resets on restart, which is fine since sessions are short-lived.

### Credential plumbing

`main.go` already loads credentials. Pass `creds.GetAPIKey("exa_ai")` to the WebSearch handler constructor. WebFetch needs no credentials.

### Prompt updates

Add `WebFetch` and `WebSearch` tool docs to all 4 prompt files. Add guidance like "Search before assuming when asked about external APIs, libraries, or current information."

## Implementation Plan

| Step | File(s) | Description |
|------|---------|-------------|
| 1 | `internal/mcp_tools/web.go` | `WebFetch` tool + handler (HTTP GET, HTML cleaning) |
| 2 | `internal/mcp_tools/web.go` | `WebSearch` tool + handler (Exa API client) |
| 3 | `internal/mcp_tools/web_test.go` | Tests for both tools |
| 4 | `cmd/symb/main.go` | Register both tools, pass Exa API key |
| 5 | `internal/llm/*.md` | Add tool docs + "search before assuming" guidance to all 4 prompts |
| 6 | `go.mod` | Add `golang.org/x/net` if needed for HTML parsing |
| 7 | `make lint && make test` | Verify |

TODO: sqlite store?
