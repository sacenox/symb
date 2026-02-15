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

Streaming multi-turn. Up to 20 tool rounds per turn. Calls
`Provider.ChatStream()` — returns `<-chan StreamEvent`. Forwards deltas via
`OnDelta` callback for incremental TUI rendering, emits complete messages via
`OnMessage` callback. Tool call limit is handled by forcing the llm to give a status
report.

Prompt system: model-specific base prompts (Claude, Gemini, Qwen, GPT).
`AGENTS.md` support: walks directory tree upward collecting all AGENTS.md files,
prepends to system prompt. Checks `~/.config/symb/AGENTS.md` too.

### Providers (`internal/provider`)

- **Ollama** — local, OpenAI-compatible `/v1` endpoint. Extracts reasoning from
  `reasoning`/`reasoning_content` fields.
- **OpenCode** — remote, API key auth. Model-specific endpoint routing.
- Both use SSE streaming with retry on initial connection (3 retries, 429/5xx).
- Single `ChatStream()` interface method replaces `Chat`/`ChatWithTools`/`Stream`.
- Deterministic JSON tool schemas, provider kv cache support

### MCP (`internal/mcp`)

Proxy merges local tool handlers with optional upstream MCP server (HTTP
Streamable-HTTP transport, SSE support, session tracking). Retry with
Retry-After parsing.

### Tools (`internal/mcptools`)

7 tools registered:

- **Read** — reads file, returns hashline-tagged content (`linenum:hash|content`). Path traversal prevention.
- **Edit** — hash-anchored file editing (replace, insert, delete, create).
  Validates hashes before modifying. Returns fresh hashes after edit. Enforces
  read-before-edit via `FileReadTracker`. Includes LSP Diagnostics.
- **Grep** — file/content search. Regex, gitignore-aware, case sensitivity,
  max results.
- **Shell** — sandboxed shell execution. Command blocking for dangerous ops,
  streaming output, timeout support.
- **TodoWrite** — LLM scratchpad/plan persistence. Visible at end of context window.
- **WebSearch** and **WebFetch** — read and search the web (search by exa.ai).

### Git Integration

- git markers in the number column for editted files in the editor (needs work but does what it's meant to do)
- TODO: Read includes diff for the file (or diff status, we need to consider token usage) edit tool includes updated file diff after change

### LSP Integration

- Show closed loop feedback on mutations done by the llm.
- Start with diagnostics (show errors/warnings in the number line, a error line has a red color number, warnings yellow).

### Basic Session storage

- table for conversation messages
- each message includes all tool calls
- opening the app opens a new session (more controls later). Same behaviour

### Tree-Sitter Context

Parse project with tree-sitter for structural awareness. Feed relevant
symbols/scope to LLM as auto-context instead of whole files.
**Uses 4k tokens on a small project like this** -- ohoh... Maybe return updated treesiter information with Grep matches for less token usage overall? Context inhection then can reduced/compacted?

### Undo!

- User should be able to undo conversation turns. The most recent entry should show a clear clickable area labelled undo.
- Clicking undo reverses context history, tool calls, filesystem changes, file changes. Resets the conversation to that exact point

### Shell Execution Tool

Run commands in sandbox (container isolation or restricted shell). Command
whitelisting. Output streaming to conversation.
Needs an undo.

### TUI Rendering loop

- 60fps baseline
- Optimized frame render logic.
- Integrates with hl theme.

### Context management

1. Context token count: input, output, and total tokens. Store them in db. Shown each message with the timestamp:

```
<time elapsed> <timestamp> <toekns in current count>/<tokens out current count> (<total tokens in context at current count).
<undo if most recent agent message>
```

Refactor so it shows two lines as above with text right aligned. Remove existing `---` separator completly
**DONE**

- Read tool caps output at 500 lines / 20k chars; tells LLM to use line ranges for larger files.
- Old tool results are compacted to their header line after the assistant responds, preventing resend of full payloads on subsequent rounds.

2. Gather baseline data with no context management. Then study:
   **INPROGRESS**

https://manus.im/blog/Context-Engineering-for-AI-Agents-Lessons-from-Building-Manus
(Search for more resources like the one above, build a document with concise knowledge base (500 lines max)

Implement findings?

### Statusbar implementation

(in order: left to right)

Left:

- Show current branch + dirty status in status bar. (needs to be updated every X frames/time) to be responsive
- Show lsp warnings and errors count for opened editor content, if any.
- Show path/filename for opened file if any and there are no LSP warnings/errors. (NOT DONE)

Right (right aligned text)

- Network errors to providers (llm, and exa_search), truncated.
  - NOTE: Animated icon becomes red until next successful request.
- Current llm provider config name
- Animated icon:
  - Slow animation when idle.
  - Fast animation while a request to the LLM is in-flight (whole turn)

### File Search modal

Opens via keybind: <ctrl + f>

File search modal:

- Centered in the UI, 80% of legth and width of the main app window, resizable with the rest of the app.
- Top row is the input for the file search query
- Rest of the modal is the list of matches.
- Results update after the user stops typing for a few hundred miliseconds.
- Up/down arrow select from the search results. If focus is on input and user presses down arrow, it focus the list. If focus is on first result and user presses up arrow focus the input.
- `Enter` on input selects the first match closing the modal
- `Enter` on result list row selected the selected row. closing the modal
- `ESC` cancels the file search closing the modal.

Selection is opened in the editor.

Make a generic modal with inputbox + list combo for re-use. The use that to make the file search one.
(It will be used for a command box maybe)

### Session cli flags:

- `-s`, `--session`: Takes a session id
- `-l`, `--list`: Lists sessions with id, last user message timestamp, and 50 characters of the last user user message.
- `-c`, `--continue`: continues last session (most recent user message)

## Features waiting implementation for current version:

Testing current feature set

### Code viewer pane -> Becomes Code editor pane

- Enable the same editting abilities as the agent input box.
- <ctrl + s> Saves the file. (Do we just save it, or we send a message to the LLM to apply the edit?)

# TODO (after features)

2. Investigate if turn latency has improved

---

## Next version plans:

### Git write commands and worktrees

- sandboxed git worktrees for agents edits.

### Human-in-the-Middle Tool Approval

Pause before executing tool calls. Show tool name + args in a dialog. User
approves/rejects. Configurable per-tool permissions in `config.toml` (allow,
ask, deny). Some tools (Read/Grep) default allow, mutations (Edit) default ask.

### Sub-Agent Tool

Spawn a child LLM turn scoped to a single task. Useful for parallel work
or decomposing complex operations.

### Parallel Tool Execution

Execute multiple independent tool calls concurrently within a single LLM
turn. Needs careful coordination with FileReadTracker and TUI updates.

### Editor-LLM link:

LLM input (in the spirit of the app, symbiotic):

- User types in agent input
- with `@cursor` or `@selected` -- Works with `@` filesearh modal.
- creates a reference with hashes of where the user's cursor or selection is in the filesystem/file

### UI Polish

- Empty-state decoration in conversation pane and editor

### Tests

Logs should be in app data home dir, not in .local

29 tests, all passing. Coverage: hashline 96.7%, filesearch 76%,
mcp_tools 43.2%, tui/editor 42.1%, tui 41.9%.
