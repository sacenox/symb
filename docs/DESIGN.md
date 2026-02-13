# Symb (pronounced "sim")

Symbiotic pairing between developer and LLM. TUI with an agentic conversation
on one side, code editor on the other. Fast, low memory, not a distraction.

## What It Is Now

### TUI (BubbleTea, ELM architecture)

Split-pane layout: editor left, conversation + input right. Draggable divider.
Click-to-focus between panes. Alt-screen, mouse cell-motion mode with 15ms
throttle on wheel/motion events.

```
╭──────────────────────┬───────────────────────╮
│ editor (read-only)   │ conversation log      │
│ - chroma highlighting│ - reasoning (muted)   │
│ - line numbers       │ - → tool calls        │
│ - soft line wrap     │ - ← tool results      │
│ - mouse scroll/select│ - content             │
│ - click, drag, copy  │ - scroll, select, copy│
│                      ├───────────────────────┤
│                      │ input (editable)      │
├──────────────────────┴───────────────────────┤
│ status bar with spinner                      │
╰──────────────────────────────────────────────╯
```

Editor component (`internal/tui/editor`): full editing capability (insert,
delete, paste, tab indent) gated behind `ReadOnly` flag. Left pane is read-only
viewer. Input pane uses same component with `ReadOnly=false`.

### LLM Loop (`internal/llm`)

Synchronous multi-turn. Up to 20 tool rounds per turn. Calls
`Provider.ChatWithTools()` — request/response, not streaming. Emits messages
via `OnMessage` callback to TUI.

Prompt system: model-specific base prompts (Claude, Gemini, Qwen, GPT).
`AGENTS.md` support: walks directory tree upward collecting all AGENTS.md files,
prepends to system prompt. Checks `~/.config/symb/AGENTS.md` too.

### Providers (`internal/provider`)

- **Ollama** — local, OpenAI-compatible `/v1` endpoint. Extracts reasoning from
  `reasoning`/`reasoning_content` fields.
- **OpenCode** — remote, API key auth. Model-specific endpoint routing.
- Both have retry logic (3 retries, 429/5xx handling).
- `Stream()` method exists on both but is needed for streaming responses feature.

### MCP (`internal/mcp`)

Proxy merges local tool handlers with optional upstream MCP server (HTTP
Streamable-HTTP transport, SSE support, session tracking). Retry with
Retry-After parsing.

### Tools (`internal/mcp_tools`)

3 tools registered:

- **Open** — reads file, returns hashline-tagged content (`linenum:hash|content`),
  sends to TUI editor. Path traversal prevention.
- **Edit** — hash-anchored file editing (replace, insert, delete, create).
  Validates hashes before modifying. Returns fresh hashes after edit. Enforces
  read-before-edit via `FileReadTracker`.
- **Grep** — file/content search. Regex, gitignore-aware, case sensitivity,
  max results.

### Supporting Packages

- `internal/hashline` — SHA-256 truncated to 2 hex chars per line. Anchor
  validation, range validation. 7 tests, 96.7% coverage.
- `internal/filesearch` — directory walker with `.gitignore` support, regex
  matching, binary detection, 10MB size limit.
- `internal/config` — TOML config, JSON credentials, env overrides.

### Dead Code

- `internal/tui/textarea/` — 1636-line bubbles/textarea fork. Imported by nothing.

## Considered Features

### Streaming Responses

Stream LLM output token-by-token to the conversation pane. `Stream()` already
exists on providers. Needs: wire into LLM loop, incremental conversation
rendering, handle tool calls mid-stream.

### Basic keybind to toggle cursor from agent input <> editor

Choose a convenient keybind to toggle the active cursor.
Goes back and forth
Use vim's `CTRL+w` keybind for easy muscle memory?

### Clickable File References

File paths in conversation (tool calls, content) become clickable. Clicking
opens the file in the editor pane at the referenced line.
- Don't show tool response bodies in conversation pane once the user can click to see. Show a max 5 line preview for the user to click to see more. User clicks, the content of the tool response is sent to the editor.

### Statusbar implementation

- Needs design work


### Web/Search Tools

HTTP fetch cleans the html for less wasted tokens, preserving content.
Search APIs from Exa.ai. Give the agent access to documentation and external resources.
 - Re-use the existing `credentials.json` file for exa api key
 - Encourage LLMs to search before assuming in prompts

### File Search in agent input box

Typing `@` shows a file search modal. Substiture `@` in the input with the selected pathname
File search modal:
 - Centered in the UI, 80% of legth and width of the main app window, resizable with the rest of the app.
 - Top row is the input for the file search query
 - Rest of the modal is the list of matches.
 - Results update after the user stops typing for a few hundred miliseconds.
 - Up/down arrow select from the search results. If focus is on input and user presses down arrow, it focus the list. If focus is on first result and user presses up arrow focus the input.
 - `Enter` on input selects the first match
 - `Enter` on result list row selected the selected row.

### Copy/Selection Improvements

Keyboard-driven selection (Shift+arrows). `Ctrl+Shift+C` to copy.
Currently mouse-only, and has bugs. Complete refactor of selection with the mouse to work in tandem with keyboard selection.

### Editor-LLM link:

LLM input (in the spirit of the app, symbiotic):
 - User types in agent input
 - with `@cursor` or `@selected` -- Works with `@` filesearh modal.
 - creates a reference with hashes of where the user's cursor or selection is in the filesystem/file

### LSP Integration

- Start with diagnostics (show errors/warnings in editor gutter).
- Go-to-definition on click. -- Needs more design before work starts
- Find references on click. -- Needs more design before work starts
- Candidates: `go.lsp.dev/protocol` or `github.com/sourcegraph/go-lsp`.

### Tree-Sitter Context

Parse project with tree-sitter for structural awareness. Feed relevant
symbols/scope to LLM as auto-context instead of whole files.

### Git Integration

- Show current branch + dirty status in status bar.
- git **read** tools for the LLM: diff, status
- Editor displays diffs with syntax hl
- git markers in the number column for editted files in the editor
- sandboxed git worktrees for agents edits.
- git **write** tools. (only after worktrees are functional)

### Tool improvements:

Separate OpenForUser into Read and Show. Read sends the output to the llm, Show sends it to the editor after read.

- Open (or as it's called internally open for user): Change to Read, shows the read output to the llm. User can click to see if he wants, no automatic loading it to the editor.
- Show, new tool: Open and send to the editor.

### Human-in-the-Middle Tool Approval

Pause before executing tool calls. Show tool name + args in a dialog. User
approves/rejects. Configurable per-tool permissions in `config.toml` (allow,
ask, deny). Some tools (Read/Grep) default allow, mutations (Edit) default ask.

### Shell Execution Tool

Run commands in sandbox (container isolation or restricted shell). Command
whitelisting. Output streaming to conversation.


### Sub-Agent Tool

Spawn a child LLM turn scoped to a single task. Useful for parallel work
or decomposing complex operations.

### Parallel Tool Execution

Execute multiple independent tool calls concurrently within a single LLM
turn. Needs careful coordination with FileReadTracker and TUI updates.

### UI Polish

- Show tool call arguments expanded: `Grep(pattern="...", ...)`, for all tools.
- Empty-state decoration in conversation pane.

### Tests

29 tests, all passing. Coverage: hashline 96.7%, filesearch 76%,
mcp_tools 43.2%, tui/editor 42.1%, tui 41.9%.

