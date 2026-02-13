# Selection & Clipboard Refactor

## Requirements

- **Keyboard selection**: Hold Shift to start selection. Text movements update selection while Shift is held. Editors only (main + agent input).
- **Mouse selection**: Dragging over text selects continually until drag stops. Works on all text in the app (editor, conversation, input).
- **Ctrl+Shift+C**: Copy selection to clipboard.
- **Ctrl+Shift+V**: Paste on active cursor.

## Current State (Pre-Refactor)

Two separate half-baked selection systems:

1. **Editor** (`editor/editor.go`): Tracks `selectStart`/`selectEnd` as `pos{row,col}` but View() never renders the highlight. Auto-copies on mouse release then immediately clears. No keyboard selection.

2. **Conversation** (`tui.go`): Line-level selection (1D wrapped-line index). Auto-copies on release then clears. No character granularity.

Both auto-copy on mouse release. No Ctrl+Shift+C/V. ctrl+c = quit. ctrl+v = paste (editor only).

## Architecture

### Editor (`editor/editor.go`)

| Change | Description |
|--------|-------------|
| Selection rendering | `View()` must highlight selected text using a selection background style |
| Keyboard selection | Shift+arrow/home/end/pgup/pgdown extends selection from cursor anchor |
| Don't auto-copy on mouse release | Keep selection visible until explicitly copied or cleared |
| Clear on non-shift movement | Unmodified arrows/clicks clear selection |
| Delete selection on edit | Typing/backspace/paste replaces selected text |
| Ctrl+Shift+C | Copy selection to clipboard |
| Ctrl+Shift+V | Paste from clipboard |
| Selection style | Add `SelectionStyle lipgloss.Style` field |

### Top-level TUI (`tui.go`)

| Change | Description |
|--------|-------------|
| Ctrl+Shift+C | Global: copy from whichever component has selection (editor, input, or conversation) |
| Ctrl+Shift+V | Forward to focused editor component |
| ctrl+c stays as quit | No change |
| Conversation selection persistence | Don't auto-copy on release; keep highlight until Ctrl+Shift+C or click elsewhere |
| Conversation mouse selection | Keep line-level but don't auto-clear |

### Key Bindings

| Shortcut | Context | Action |
|----------|---------|--------|
| Shift+arrows | Editor (focused) | Start/extend selection |
| Shift+Home/End | Editor (focused) | Select to line start/end |
| Shift+PgUp/PgDown | Editor (focused) | Select page up/down |
| Ctrl+Shift+C | Global | Copy selection to clipboard |
| Ctrl+Shift+V | Editor (focused) | Paste from clipboard |
| Mouse drag | Anywhere | Select text (keep visible after release) |
| Click (no drag) | Anywhere | Clear selection + position cursor |
| Any edit key | Editor (focused) | Delete selection first, then apply edit |

### Exported API (editor -> tui)

- `HasSelection() bool` — tui queries for Ctrl+Shift+C routing
- `SelectedText() string` — tui reads for clipboard copy
- `ClearSelection()` — tui clears after copy or focus change
- `SelectionStyle lipgloss.Style` — set by parent

### Risk Areas

- Shift key detection: bubbletea uses `"shift+left"`, `"shift+right"`, `"ctrl+shift+c"`, etc.
- Selection rendering in View(): must intersect selection range with each visual row, handle soft-wrap, syntax highlighting, and cursor.
- Editor mouse currently gated on `m.focus` — mouse press in tui.go already focuses editor before forwarding, so this is fine.
