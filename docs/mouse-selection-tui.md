# Mouse & Selection in Bubbletea TUIs

Reference document based on patterns from Charm's Crush (production TUI using
Bubbletea v2 + Lipgloss v2). Applies directly to our stack.

---

## 1. Mouse Modes

Bubbletea supports two mouse tracking modes:

- **CellMotion** (`tea.WithMouseCellMotion()` / `MouseModeCellMotion`): reports
  presses, releases, and motion *while a button is held* (i.e. drags). This is
  what you want for selection. Does NOT report hover.
- **AllMotion** (`tea.WithMouseAllMotion()` / `MouseModeAllMotion`): also
  reports motion with no button held. Expensive — avoid unless hover is needed.

Crush uses CellMotion. So do we.

## 2. Mouse Message Types (Bubbletea v2)

Bubbletea v2 splits the old `tea.MouseMsg` into distinct types:

| Type                | When fired                       |
|---------------------|----------------------------------|
| `tea.MouseClickMsg` | Button pressed                   |
| `tea.MouseMotionMsg`| Motion while button held (drag)  |
| `tea.MouseReleaseMsg`| Button released                 |
| `tea.MouseWheelMsg` | Scroll wheel                     |

Bubbletea v1 (what we use) has a single `tea.MouseMsg` with `.Action` field:

| `msg.Action`              | Equivalent v2 type    |
|---------------------------|-----------------------|
| `tea.MouseActionPress`    | `tea.MouseClickMsg`   |
| `tea.MouseActionMotion`   | `tea.MouseMotionMsg`  |
| `tea.MouseActionRelease`  | `tea.MouseReleaseMsg` |

And `msg.Button` for wheel: `tea.MouseButtonWheelUp`, `tea.MouseButtonWheelDown`.

## 3. Event Throttling

Trackpads and high-refresh mice flood the event queue. Use `tea.WithFilter` to
throttle motion and wheel events at the program level:

```go
var lastMouseEvent time.Time

func MouseEventFilter(_ tea.Model, msg tea.Msg) tea.Msg {
    switch msg.(type) {
    case tea.MouseWheelMsg, tea.MouseMotionMsg:
        now := time.Now()
        if now.Sub(lastMouseEvent) < 15*time.Millisecond {
            return nil // drop event
        }
        lastMouseEvent = now
    }
    return msg
}
```

For v1 (our code), switch on `tea.MouseMsg` and check `msg.Action`/`msg.Button`
instead. **Never drop click or release events** — only motion and wheel.

Register at program creation:

```go
p := tea.NewProgram(model, tea.WithMouseCellMotion(), tea.WithFilter(filter))
```

## 4. Mouse Event Routing

### 4.1 Layout Rects (Not Zones)

Both Crush and our codebase use `image.Rectangle` structs for layout regions.
Hit testing is `image.Pt(msg.X, msg.Y).In(rect)`. This is simpler and more
predictable than lipgloss zone-based routing.

### 4.2 Coordinate Translation

Mouse coordinates are screen-absolute. Child components expect local
coordinates. Subtract the layout rect origin before forwarding:

```go
localX := msg.X - layout.pane.Min.X
localY := msg.Y - layout.pane.Min.Y
```

### 4.3 Routing Priority

Process mouse events in this order:

1. **Dialogs/overlays** — if open, they consume all mouse events
2. **Divider drag** — if resizing, motion goes to resize logic
3. **Focus switching** — click sets focus to the clicked region
4. **Component dispatch** — forward (with coord translation) to the hit region

This matches both Crush and our current approach.

## 5. Selection Data Model

### 5.1 Anchor + Active Pattern

Selection is defined by two points:

- **Anchor**: where the selection started (mouse down, or cursor position when
  shift was first held)
- **Active**: where the selection currently extends to (mouse drag position, or
  current cursor position while shift is held)

The anchor is fixed; the active point moves. This lets backward selection
(dragging up/left) work naturally.

```go
type selPos struct{ row, col int }

type selection struct {
    anchor selPos
    active selPos
}
```

To get the ordered range:

```go
func (s selection) ordered() (start, end selPos) {
    if s.anchor.row > s.active.row ||
       (s.anchor.row == s.active.row && s.anchor.col > s.active.col) {
        return s.active, s.anchor
    }
    return s.anchor, s.active
}
```

### 5.2 Valid Selection Check

A selection is "empty" when anchor == active. Only render highlight and allow
copy when they differ.

### 5.3 Clearing Selection

Clear on:
- Any non-shift cursor movement (arrows, home, end, pgup, pgdown)
- Click (press) without drag
- Edit operations (typing, backspace, delete, paste)
- Explicit copy (Ctrl+Shift+C) — optional, some editors keep it

Do NOT clear on:
- Mouse motion during drag
- Shift+movement keys
- Scroll (wheel)

## 6. Mouse Selection Flow

### 6.1 Press → Drag → Release

```
Press:   Set anchor = active = screenToPos(x, y). Set mouseDown = true.
Motion:  If mouseDown, update active = screenToPos(x, y).
Release: Set mouseDown = false. Keep selection visible.
```

Crush does NOT auto-copy on release. It waits for an explicit copy action
(keyboard shortcut or delayed auto-copy after double-click timeout). This
prevents accidental clipboard overwrites during casual clicking.

### 6.2 Click Detection (No Drag)

If press and release happen at the same position (anchor == active after
release), treat it as a click, not a selection. Clear any existing selection
and handle the click action (e.g., position cursor, open file link).

### 6.3 Double-Click / Triple-Click

Crush implements multi-click via timing + spatial tolerance:

```go
const (
    doubleClickThreshold = 400 * time.Millisecond
    clickTolerance       = 2 // pixels
)
```

On click, check if the previous click was within threshold and tolerance:
- **Double-click**: select word at cursor (using Unicode word boundaries)
- **Triple-click**: select entire line

Track `lastClickTime`, `lastClickX`, `lastClickY`, `clickCount`.

### 6.4 Delayed Single-Click

Problem: a single click should position the cursor, but the first click of a
double-click should NOT trigger single-click behavior.

Solution: on single click, schedule a delayed action after `doubleClickThreshold`.
Increment a `pendingClickID`. If a double-click arrives before the delay fires,
the ID won't match and the pending action is discarded.

### 6.5 Auto-Scroll During Drag

When dragging near viewport edges, scroll the content:

```go
case tea.MouseActionMotion:
    if msg.Y <= 0 {
        scrollUp(1)
    } else if msg.Y >= viewportHeight-1 {
        scrollDown(1)
    }
```

This lets the user drag-select beyond the visible area.

## 7. Keyboard Selection (Editors)

Keyboard selection uses the same anchor+active model:

- **Start selection**: On any shift+movement key, if no selection exists, set
  anchor = current cursor position. Then move cursor. Set active = new cursor.
- **Extend selection**: On shift+movement with existing selection, just move
  cursor and update active. Anchor stays put.
- **Clear selection**: On any non-shift movement or edit, clear the selection.

Supported shift+key combinations:

| Key             | Action                    |
|-----------------|---------------------------|
| `shift+left`    | Extend selection left 1   |
| `shift+right`   | Extend selection right 1  |
| `shift+up`      | Extend selection up 1 row |
| `shift+down`    | Extend selection down 1   |
| `shift+home`    | Extend to line start      |
| `shift+end`     | Extend to line end        |
| `shift+pgup`    | Extend up by page         |
| `shift+pgdown`  | Extend down by page       |

## 8. Clipboard

### 8.1 Dual Write for Compatibility

Use both OSC 52 and native clipboard for writes:

```go
func CopyToClipboard(text string) tea.Cmd {
    return tea.Sequence(
        tea.SetClipboard(text),          // OSC 52 (works through SSH/tmux)
        func() tea.Msg {
            _ = clipboard.WriteAll(text)  // Native OS clipboard
            return nil
        },
    )
}
```

`tea.SetClipboard()` sends an OSC 52 escape sequence. Many terminals support
it, and it works through SSH tunnels and tmux (if configured). The native
clipboard call (`github.com/atotto/clipboard`) covers local-only terminals.

### 8.2 Copy Trigger

Ctrl+Shift+C (not Ctrl+C, which should remain quit/cancel). The top-level
handler checks which component has a selection and copies from it.

### 8.3 Paste Trigger

Ctrl+Shift+V. Read from native clipboard and insert at cursor.

## 9. Highlight Rendering

### 9.1 Cell-Level Approach (Crush)

Crush uses `ultraviolet.ScreenBuffer` — a cell grid where each cell has content,
foreground, background, and attributes. Highlighting replaces cell styles in the
selected range. This gives perfect per-character accuracy but requires the UV
library.

### 9.2 ANSI String Approach (Our Stack)

Without UV, we work with ANSI-styled strings. To highlight a region within a
rendered line:

1. Determine which characters fall within the selection range
2. Split the line into before / selected / after segments
3. Strip existing background from the selected segment
4. Apply the selection background style
5. Reassemble

For syntax-highlighted content, use `ansi.Cut(hlString, start, end)` to extract
ANSI substrings at character boundaries, then wrap the selected portion in the
highlight style.

### 9.3 Selection Style

Define a visible but not jarring selection background:

```go
Selection: lipgloss.NewStyle().Background(lipgloss.Color("#264f78"))
```

Dark blue (#264f78) is the VS Code default and works well on dark backgrounds.
The current `#111111` is too subtle — almost invisible.

### 9.4 Per-Row Intersection

For each visual row during render, compute the intersection with the selection
range:

```go
// selStart, selEnd are in buffer coordinates (row, col)
// vr.bufRow is this visual row's buffer line
// vr.segStart, vr.segEnd are the rune range of this segment

if vr.bufRow < selStart.row || vr.bufRow > selEnd.row {
    // No selection on this row
} else {
    colStart := 0
    if vr.bufRow == selStart.row { colStart = selStart.col - vr.segStart }
    colEnd := segLen
    if vr.bufRow == selEnd.row { colEnd = selEnd.col - vr.segStart }
    // Clamp to segment bounds, then render highlight
}
```

## 10. Cross-Component Selection

Our app has 3 selectable regions: editor, conversation, input. Only one can
have an active selection at a time. When a mouse press or shift+key starts a
selection in one region, clear any selection in the others.

The top-level model owns this coordination:

```go
func (m *Model) clearAllSelections() {
    m.editor.ClearSelection()
    m.agentInput.ClearSelection()
    m.convSelection = nil
}
```

On Ctrl+Shift+C, check each component in priority order and copy from the
first one with an active selection.

## 11. Common Pitfalls

1. **Auto-copy on mouse release**: Don't. It overwrites the clipboard on every
   casual click-drag. Copy should be explicit (Ctrl+Shift+C) or delayed.

2. **Clearing selection on release**: Don't. The user expects to see what they
   selected, then press a key to copy. Only clear on next click or edit.

3. **Not translating coordinates**: Mouse events use screen-absolute positions.
   Always subtract the component's layout origin before forwarding.

4. **Selection during scroll**: Mouse wheel should NOT affect selection state.
   The selection anchors are in buffer coordinates, not screen coordinates, so
   they survive scrolling.

5. **Forgetting to clamp**: Selection endpoints must be clamped to valid buffer
   ranges. Off-by-one errors here cause panics or garbled rendering.

6. **Highlight on trailing whitespace**: Crush explicitly avoids highlighting
   trailing whitespace on each line. Track `lastContentX` and only highlight
   up to that point. This looks much cleaner.
