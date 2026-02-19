# Symb (pronounced "sim")

Symbiotic pairing between developer and LLM. TUI with an agentic conversation
on one side, code editor on the other. Fast, low memory, not a distraction.

## What It Is Now

### TUI (BubbleTea, ELM architecture)

Alt-screen, mouse cell-motion mode with 15ms
throttle on wheel/motion events.

```
╭─────────────────────────────────────────────╮
│ conversation log                            │
│ - reasoning (muted)                         │
│ - → tool calls                              │
│ - ← tool results                            │
│ - content                                   │
│ - scroll, select, copy                      │
├─────────────────────────────────────────────┤
│ input (editable)                            │
├─────────────────────────────────────────────┤
│ status bar with spinner                     │
╰─────────────────────────────────────────────╯
```

// TODO: Remove editor pane. keep the editor for agent input
Editor component (`internal/tui/editor`): full editing capability (insert,
delete, paste, tab indent) gated behind `ReadOnly` flag. Left pane is read-only
viewer. Input pane uses same component with `ReadOnly=false`.

Make a generic modal with inputbox + list combo for re-use. Then use that to make the file search one.
(It will be used for a command box maybe)

// TODO: Drop editor pane, use full with.
//       Bigger refactor, means making a modal to view tool results
//       Filesearch modal now becomes an autocomple, an inserts the file into the prompt when sent. User triggers the modal with `@`

Conversation log, an interactive conversation log where the user can click on tool messages to open a modal
and view the contents of that tool call (call and results). Streams results in modal if tool is running.


### Agent input interactivity:

Agent input, simple multiline textarea with markdown highlighting.

Use a special key `@` to spawn an autocomplete/filesearch modal. This can match files, skills, commands or subagents.
When the user selects a match with `Enter` it replaces the @ with the selected item once the message is sent to the, includes hashes when injected into the user message.
Start with file search only for the initial implementation.

### Search modal

Opens via keybind: `@` in the agent input

Search modal:

- Centered in the UI, 80% of length and width of the main app window, resizable with the rest of the app.
- Top row is the input for the file search query
- Rest of the modal is the list of matches.
- Results update after the user stops typing for a few hundred miliseconds.
- Up/down arrow select from the search results. If focus is on input and user presses down arrow, it focus the list. If focus is on first result and user presses up arrow focus the input.
- `Enter` on input selects the first match closing the modal
- `Enter` on result list row selected the selected row. closing the modal
- `ESC` cancels the file search closing the modal.

Selection is inserted into the user message on send at the place of the `@`.

### Statusbar implementation

Status bar, a simple bar showing the current git status current model and animated icon

(in order: left to right)

Left:

- Show current branch + dirty status in status bar. (needs to be updated every X frames/time) to be responsive

Right (right aligned text)

- Current llm provider config name
- Animated icon:
  - Slow animation when idle.
  - Fast animation while a request to the LLM is in-flight (whole turn)
  - Animated icon becomes red on network errors until next successful request.

### Other modals:

- Select active model (lists all available models from all providers)
- Show keybinds/help

### LLM Loop (`internal/llm`)

Streaming multi-turn. Up to 20 tool rounds per turn. Calls
`Provider.ChatStream()` — returns `<-chan StreamEvent`. Forwards deltas via
`OnDelta` callback for incremental TUI rendering, emits complete messages via
`OnMessage` callback. Tool call limit is handled by forcing the llm to give a status
report.

Prompt system: model-specific base prompts (Claude, Gemini, Qwen, GPT).
`AGENTS.md` support: walks directory tree upward collecting all AGENTS.md files,
prepends to system prompt. Checks `~/.config/symb/AGENTS.md` too.

// TODO: guard against massive folders needed.

### Providers (`internal/provider`)

- **Ollama**, **Zen** — local, and remote.
- SSE streaming with retry on initial connection (3 retries, 429/5xx).
- Single `ChatStream()` interface method replaces `Chat`/`ChatWithTools`/`Stream`.
- Deterministic JSON tool schemas, provider kv cache support

### MCP (`internal/mcp`)

Proxy merges local tool handlers with optional upstream MCP server (HTTP
Streamable-HTTP transport, SSE support, session tracking). Retry with
Retry-After parsing.

Used for exa.ai tools

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
- **WebSearch** and **WebFetch** — read and search the web (by exa.ai via mcp).

### Git Integration

// TODO: Reconsider git integration without the editor pane
//       Use git diffs and worktrees for edits?

- git markers in the number column for editted files in the editor (needs work but does what it's meant to do)
- TODO: Read includes diff for the file (or diff status, we need to consider token usage) edit tool includes updated file diff after change

### LSP Integration

- Show closed loop feedback on mutations done by the llm.
- Start with diagnostics (show errors/warnings in the number line, a error line has a red color number, warnings yellow).

### Basic Session storage

- table for conversation messages
- each message includes all tool calls
- opening the app opens a new session (more controls later). Same behaviour
- modal to resume past sessions

### Tree-Sitter Context

// TODO: remove context injection. Use project treesitter info to improve Grep tool

Parse project with tree-sitter for structural awareness. Feed relevant
symbols/scope to LLM as auto-context instead of whole files.
**Uses 4k tokens on a small project like this** -- ohoh... Maybe return updated treesiter information with Grep matches for less token usage overall? Context inhection then can reduced/compacted?

### Undo!

- User should be able to undo conversation turns. The most recent entry should show a clear clickable area labelled undo.
- Clicking undo reverses context history, tool calls, filesystem changes, file changes. Resets the conversation to that exact point

### Interrupt!

// TODO: Interrupted messages should still show the message footer in the TUI

- User can interrupt the LLM on exit or via `ESC` when an assitant is replying

### Shell Execution Tool

Run commands in sandbox (container isolation or restricted shell). Command
whitelisting. Output streaming to conversation.

### TUI Rendering loop

- 60fps baseline
- Optimized frame render logic.
- Integrates with hl theme.

### Context management

1. Context token count: input, output, and total tokens. Store them in db. Shown each message with the timestamp:

```
<time elapsed> <timestamp> <tokens in current count>/<tokens out current count> (<total tokens in context at current count).
<undo if most recent agent message>
```

**DONE**

- Read tool caps output at 500 lines / 20k chars; tells LLM to use line ranges for larger files.

2. Gather baseline data with no context management. Then study:
   **INPROGRESS**

https://manus.im/blog/Context-Engineering-for-AI-Agents-Lessons-from-Building-Manus
(Search for more resources like the one above, build a document with concise knowledge base (500 lines max)

Implement findings?

### Session cli flags:

- `-s`, `--session`: Takes a session id
- `-l`, `--list`: Lists sessions with id, last user message timestamp, and 50 characters of the last user user message.
- `-c`, `--continue`: continues last session (most recent user message)

### Sub-Agent Tool

Spawn a child LLM turn scoped to a single task. Useful for parallel work
or decomposing complex operations.

- Have a short max iteration
- Customized prompt for single purpose agents
- TODO: Custom agents from community configs
- TODO: Worktress so they don't colide (needs to be an option, not all users use or like gitworktrees)

Testing, cleaning up.

## Features waiting implementation for current version:

### Community configs support:

- CLAUDE/AGENTS/CURSOR/ETC.md (or just AGENTS.md at this point?) -- Is there a standard?
- Custom subagent prompts for custom agents
- Commands and skills implementation to match community expectations

### Tool hooks:

Like git hooks, but after a specific tool call (lint after edit, or format when Y, etc).

## Next version plans:

### Git write commands and worktrees

- sandboxed git worktrees for agents edits.

### Human-in-the-Middle Tool Approval

Pause before executing tool calls. Show tool name + args in a dialog. User
approves/rejects. Configurable per-tool permissions in `config.toml` (allow,
ask, deny). Some tools (Read/Grep) default allow, mutations (Edit) default ask.

### UI Polish

- Empty-state decoration in conversation pane and editor
