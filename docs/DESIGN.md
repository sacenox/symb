# Symb (pronounced "sim")

A code/pair programming tool for developers who use agents in their main workflows. Symbiotic pairing between developer and LLM. The developer has an agentic UI that provides the interface to the LLM, and a set of tools the LLM can use to cooperate in coding/development with the user.

Simple UI. Being a TUI, the goal is to be fast, have low memory usage, and avoid being a distraction to the code/content. Simple unicode border around the window. Agentic conversation on one side, code editor on the other. Matrix green for color (used rarely, only when a color is absolutely needed).

A TUI from the future. We want feature parity with a GUI editor: mouse selection, clicking, etc.

## Minimum viable product:

- Agentic UI:
  - Support for clicking file references/diffs/snippets to open in Editor side.
  - Optional Human-in-the-middle LLM tool call loop (with configurable permissions in global config)
  - Auto Context, LLM is aware of file/project in question probably via tree-sitter?
  - AGENTS.md support

- Code/Diff viewer UI:
  - Line numbers, syntax highlighting
  - LSP support
  - Git integration (show file git status)
  - Simple editting support, no advanced usages or keybinds. ** DONE **
  - Code browsing mouse features: click to go to definition/references. Search for word under cursor.

- LLM Tools:
  - Run in sandboxed version of the working directory to allow multiple agents working on the same file. Use git to merge sandboxed changes to real repo.
  - Grep, Read, Write: optimized for code changes (LSP and linting checks after each edit). These appear in agentic UI as a diff message (not actual diff). Clicking on it takes us to that edit in the editor tab.
  - Undo: Either perform an intelligent step back or use git to reset.
  - Subagent tool: Performs a single task based on prompt.
  - Mutation tools create a new branch worktress for the sandboxed environment. Perform the mutation. Once the user approves the mutation or tells the agent to merge, a git merge happens.

- Features:
  - `<CTRL> + <f>`: Search files by filename or content (fuzzy matches, fast)
    - Opens a modal/overlay with a search box and a dynamic list of matches.
    - Searching filters the list.
    - Up/Down arrows select item in the list.
    - Enter opens the currently matched/selected file.
    - Includes the currently "opened" buffers in search at the top of the matches.
    - We should find a library that does this for us, before trying to code our own fuzzy

  - `<CTRL> + <g>`: Open git diff in the editor.

  - Selected text + `<CTRL> + <k>`: Send selected text to the LLM input.
  - `<CTRL> + <k>`: Prompt at cursor, focus the input with information for the LLM about the user's cursor.

- Theme: Clean, straight corners, _suit and tie_-like. Use dark grayscale for almost everything on a clean black background. Matrix green used lightly (animated activity spinner). Single clean unicode lines (like in the drawing bellow)

## Mockup

```text
╭─────────────────────────────────────┬────────────────────────────────────╮
│ 102                                 │ ● Symb                            │ // Flexible height.
│ 103  pub fn wake_up(neo: &User) {   │                                    │ // Panes split 50% each
│ 104      // TODO: Implement signal  │ ┌─ Assistant ────────────────────┐ │
│ 105      let signal = Signal::new();│ │ I see you're working on the    │ │ // Assistant message
│ 106                                 │ │ wake_up function.              │ │ // - Messages have their embedded code highlighter for code blocks, diffs etc.
│ 107      if neo.is_ready() {        │ │                                │ │ // - Shows short reasoning (toggleable in the options)
│ 108          neo.disconnect();      │ │ Would you like me to generate  │ │ // - Shows tool calls/tool response is stylized human readable format.
│ 109      } else {                   │ │ the Signal implementation?     │ │
│ 110          neo.sleep();           │ │                                │ │ // Conversation log takes a lot of inspiration from Opencode.
│ 111      }                          │ │ [Generate]  [Diff]  [Ignore]   │ │
│ 112  }                              │ └────────────────────────────────┘ │
│ 113                                 │                                    │
│ 114                                 │ ┌─ Input ────────────────────────┐ │
│ 115                                 │ │ /bash cargo check              │ │ // Input box, user can cycle history with arrows *up/down*
│ 116                                 │ └────────────────────────────────┘ │ // - Input supports commands /Read /Grep /Write (call the same tools as the LLM sees)
├─────────────────────────────────────┴────────────────────────────────────┤
│ master* │ src/matrix/core.rs[+]                                        ⣽ │ // Git branch, current file if any. ending with an
╰──────────────────────────────────────────────────────────────────────────╯ // animated icon: fast animation when LLM is thinking, slow when idle.
```

### Agent message:

```text

[reasoning ...]

→  Read(file, args)
←  Write(file, args)

[agent reply...]

XXs 00:00 ───────────────────────── // DarkGray color, timestamp and duration of the LLM response: 10s 19:57
```

### User Message

```
[User message ...]

XXs 00:00 ───────────────────────── // DarkGray color

```

## Tech:

- True ELM architecture (no exceptions ever).
  - Strict project structure with separation of concern by internal modules.
- Go (see https://github.com/tj/go-tea) For ELM architecture in TUI app (documentation).
- BubbleTea:
  - Base library for TUI, use their bubbles, and all of the ecosystem.
  - Golden files based testing for the TUI.
  - https://github.com/charmbracelet/bubbletea Root BubbleTea.
- LLM Providers: Ollama local + Opencode Zen.
- Config driven: `./config.toml`.

### References:

- ZOEA NOVA `~/src/zoea-nova`: A past project with a lot of overlap, also focused on LLM agentic loop.
- Mysis `~/src/mysis`: Also a past project: raw CLI agentic tool for a dedicated agent (lots of overlap).
- Cursor IDE (Cursor has this UX, but it's slow, leaks memory and it's more distracting than most other editors).
- Opencode (TUI in `ink` does the same without the text editor, 75% feature overlap).

### Dependencies:

- A LSP client implementation in Go.
  - Candidate: `github.com/sourcegraph/go-lsp` (Battle-tested by Sourcegraph).
  - Alternative: `go.lsp.dev/protocol` (Modern, modular).
- Mouse events/mouse support for BubbleTea.
  - Candidate: `github.com/lrstanley/bubblezone` (Wrapper for easier hit-testing/zoning).
- Editor:
  - Official: `github.com/charmbracelet/bubbles/textarea` (Basic text input).
  - Vim-like: `github.com/mieubrisse/vim-bubble` (Vim buffer emulation).
- Syntax Highlighting:
  - Candidate: `github.com/alecthomas/chroma` (Standard Go syntax highlighter).

# The Main Challenge Remaining:

State Management. You have three asynchronous "gods" to serve:

    1.  User Input (Keyboard/Mouse) -> Instant.
    2.  LSP Server (Diagnostics/Completions) -> Fast (ms).
    3.  LLM Agent (Thinking/Tools) -> Slow (seconds).

BubbleTea's ELM architecture (Update() loop) is perfect for this if you keep the heavy lifting (LLM/LSP) in separate Go routines that send messages back to the main thread. If you block the main thread, the UI will freeze.
Confidence Level: High for a prototype. The ecosystem is ready.

## MVP Phases:

1. Phase 1: Basic TUI with editor + agent pane (no LSP, no git sandbox)
   - File viewer with syntax highlighting
   - Agent conversation with tool calls (Read/Write/Grep)
   - Simple diff preview before applying
2. Phase 2: Git integration
   - Show git status in editor
   - Ctrl+g for diffs
   - Agent commits to real repo (no sandbox yet)
3. Phase 3: LSP integration
   - Start with diagnostics only
   - Add go-to-definition later
4. Phase 4: Advanced features
   - Sandboxed worktrees
   - Sub-agents
   - Context optimization with tree-sitter

# TODO:

- Conversation log ordering is all broken.
