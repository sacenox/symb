# TUI Expert Knowledge

Best practices for building polished Bubbletea TUIs, distilled from studying
Charm's production codebase (github.com/charmbracelet/crush). All patterns use
bubbletea, bubbles, lipgloss, and related Charm libraries.

References: `crush:<path>:<line>` pointing to the crush source at `/tmp/crush`.

---

## 1. Architecture

### ELM Pattern with Composed Sub-Models

Top-level `Model` owns all sub-models as struct fields. Each sub-model
exposes methods the parent calls -- the parent does all message routing.

```go
type UI struct {
    chat        *Chat           // message list
    textarea    textarea.Model  // input editor
    dialog      *Overlay        // modal stack
    completions *Completions    // popup
    status      *Status         // status bar
    layout      uiLayout        // computed rectangles
    state       uiState         // state machine (onboarding/landing/chat)
    focus       uiFocus         // which pane has focus (none/editor/main)
}
```

> crush:internal/ui/model/ui.go:42-59, 130

### Service Events via Channel Bridge

Backend services publish events through a channel. A goroutine bridges them
into bubbletea via `program.Send()`:

```go
func (a *App) Subscribe(p *tea.Program) {
    for msg := range a.events { p.Send(msg) }
}
```

> crush:internal/app/app.go:508

---

## 2. Layout

### Rectangle-Based Layout

Store layout as `image.Rectangle` fields. Compute in a pure function from
terminal dimensions. Re-compute on `WindowSizeMsg` and on every `Draw()`.

```go
type uiLayout struct {
    area, header, main, editor, sidebar, status image.Rectangle
}
```

Use `layout.SplitVertical`/`SplitHorizontal` with `layout.Fixed` constraints:

```go
mainRect, sideRect := layout.SplitHorizontal(appRect,
    layout.Fixed(appRect.Dx()-sidebarWidth))
chatRect, editorRect := layout.SplitVertical(mainRect,
    layout.Fixed(mainRect.Dy()-editorHeight))
```

After computing layout, push dimensions to every sub-model:

```go
m.chat.SetSize(m.layout.main.Dx(), m.layout.main.Dy())
m.textarea.SetWidth(m.layout.editor.Dx())
```

> crush:internal/ui/model/ui.go:2220-2240, 2244-2413

### Responsive Breakpoints

Toggle compact/full mode at breakpoints. In compact mode: sidebar disappears,
header shrinks, session details become a toggleable overlay.

```go
const (
    compactWidthBreakpoint  = 120
    compactHeightBreakpoint = 30
)
m.isCompact = m.width < compactWidthBreakpoint || m.height < compactHeightBreakpoint
```

> crush:internal/ui/model/ui.go:57-59, 2202-2218

---

## 3. Screen Buffer Rendering

Draw components into rectangular regions on a shared screen buffer instead
of concatenating strings. This enables overlapping content (dialogs, popups).

```go
func (m *UI) View() tea.View {
    canvas := uv.NewScreenBuffer(m.width, m.height)
    m.Draw(canvas, canvas.Bounds())
    return tea.View{Content: canvas.Render(), AltScreen: true}
}

func (m *UI) Draw(scr uv.Screen, area uv.Rectangle) {
    screen.Clear(scr)
    uv.NewStyledString(headerView).Draw(scr, m.layout.header)
    m.chat.Draw(scr, m.layout.main)
    // Dialogs LAST -- they paint over everything
    if m.dialog.HasDialogs() { m.dialog.Draw(scr, scr.Bounds()) }
}
```

> crush:internal/ui/model/ui.go:1823-1974

---

## 4. Mouse Support

### Mode and Throttling

Use `MouseModeCellMotion` -- motion events only fire during drag, not on
every cursor move. Rate-limit wheel/motion at the program level via
`tea.WithFilter`. Never filter clicks:

```go
v.MouseMode = tea.MouseModeCellMotion

func MouseEventFilter(m tea.Model, msg tea.Msg) tea.Msg {
    switch msg.(type) {
    case tea.MouseWheelMsg, tea.MouseMotionMsg:
        if time.Since(lastMouseEvent) < 15*time.Millisecond {
            return nil // drop
        }
        lastMouseEvent = time.Now()
    }
    return msg
}
```

> crush:internal/ui/model/filter.go:9-22, ui.go:1951

### Coordinate Translation and Hit-Testing

Mouse events arrive in screen-absolute coordinates. Translate to component-
local by subtracting layout region origin. Hit-test with `image.Point.In`:

```go
x, y := msg.X-m.layout.main.Min.X, msg.Y-m.layout.main.Min.Y

if image.Pt(msg.X, msg.Y).In(m.layout.editor) {
    m.focus = uiFocusEditor
} else if image.Pt(msg.X, msg.Y).In(m.layout.main) {
    m.focus = uiFocusMain
}
```

> crush:internal/ui/model/ui.go:580, 952-968

### Routing Priority

Every mouse event type checks dialogs first. If a dialog is open, route
exclusively to it and return early (no click-through):

```go
case tea.MouseClickMsg:
    if m.dialog.HasDialogs() {
        m.dialog.Update(msg)
        return m, tea.Batch(cmds...)
    }
    // ...normal handling with coordinate translation
```

> crush:internal/ui/model/ui.go:565-684

### Multi-Click Detection

Use a pending-click-ID pattern to distinguish single/double/triple clicks.
Single-click actions are delayed by `doubleClickThreshold` (400ms);
subsequent clicks invalidate the pending ID:

```go
m.pendingClickID++
id := m.pendingClickID
cmds = append(cmds, tea.Tick(doubleClickThreshold, func(t time.Time) tea.Msg {
    return DelayedClickMsg{ID: id}
}))
// On delayed msg: only act if ID still matches (no newer click)
```

Double-click selects a word (UAX#29 word segmentation). Triple-click selects
a line. Selection state tracked as `mouseDownItem/X/Y`, `mouseDragItem/X/Y`.

> crush:internal/ui/model/chat.go:463-562

### Mouse Text Selection

Apply highlight during render via a registered callback, not during Update.
The callback sets `item.(Highlightable).SetHighlight(...)` on each item.
Highlight uses `AttrReverse` on individual cells in the screen buffer:

```go
var DefaultHighlighter = func(x, y int, c *uv.Cell) *uv.Cell {
    c.Style.Attrs |= uv.AttrReverse
    return c
}
l.RegisterRenderCallback(c.applyHighlightRange)
```

> crush:internal/ui/list/highlight.go:13-135

---

## 5. Modal Dialogs

### Stack-Based Overlay

Dialogs managed in a slice-as-stack. Only the front dialog receives messages.
All dialogs render back-to-front (z-order painting):

```go
type Overlay struct { dialogs []Dialog }

type Dialog interface {
    ID() string
    HandleMsg(msg tea.Msg) Action  // returns typed Action, NOT tea.Cmd
    Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor
}

func (d *Overlay) Update(msg tea.Msg) tea.Msg {
    return d.dialogs[len(d.dialogs)-1].HandleMsg(msg)
}
func (d *Overlay) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
    for _, dialog := range d.dialogs { dialog.Draw(scr, area) }
}
```

> crush:internal/ui/dialog/dialog.go:30-205

### Action-Based Communication

Dialogs return typed action structs. The parent type-switches to handle them.
This is cleaner than `tea.Cmd` for dialog results:

```go
type Action any
// ActionClose, ActionSelectModel{Provider,Model}, ActionPermissionResponse{...}

action := m.dialog.Update(msg)
switch a := action.(type) {
case dialog.ActionClose:         m.dialog.CloseFrontDialog()
case dialog.ActionSelectModel:   m.switchModel(a.Provider, a.Model)
case dialog.ActionPermissionResponse: m.handlePermission(a.Permission, a.Action)
}
```

> crush:internal/ui/model/ui.go:1127-1200

### Centering and Sizing

Center dialogs with pure geometry. Scale dimensions relative to terminal
with caps. Force fullscreen when terminal is too small:

```go
func CenterRect(area image.Rectangle, w, h int) image.Rectangle {
    cx, cy := area.Min.X+area.Dx()/2, area.Min.Y+area.Dy()/2
    return image.Rect(cx-w/2, cy-h/2, cx-w/2+w, cy-h/2+h)
}
// Adaptive sizing:
width = min(int(float64(area.Dx())*0.8), 180)  // with diff
width = min(int(float64(area.Dx())*0.6), 100)  // without diff
if area.Dx() < 77 || area.Dy() < 20 { forceFullscreen = true }
```

> crush:internal/ui/common/common.go:45-56
> crush:internal/ui/dialog/permissions.go:345-401

### Standardized Dialog Layout

Use a `RenderContext` builder for consistent dialog structure: gradient title
bar, content parts with optional gaps, help bar, wrapped in rounded border:

```go
rc := NewRenderContext(styles, width)
rc.Title = "Commands"
rc.TitleInfo = radioButtons   // inline tab selector
rc.AddPart(inputView)         // text filter
rc.AddPart(listView)          // filtered list
rc.Help = helpView
view := rc.Render()           // bordered dialog with gradient title
DrawCenterCursor(scr, area, view, cursor)
```

> crush:internal/ui/dialog/common.go:42-151

---

## 6. Scrolling

### Line-Granularity Scroll Model

Track two values: index of first visible item and lines of that item above
viewport. This allows partial item visibility at the top:

```go
type List struct {
    items      []Item
    offsetIdx  int // first visible item index
    offsetLine int // lines of that item above viewport
    height     int // viewport height in lines
}
```

> crush:internal/ui/list/list.go:10-36

### Sticky-Bottom Auto-Scroll

Check `AtBottom()` before appending content. Only auto-scroll if user was
already at bottom. Scroll 5 lines per wheel tick:

```go
atBottom := m.chat.list.AtBottom()
// ...append new items...
if atBottom { m.chat.ScrollToBottom() }

case tea.MouseWheelUp:   m.chat.ScrollBy(-5)
case tea.MouseWheelDown: m.chat.ScrollBy(5)
```

> crush:internal/ui/model/ui.go:652-684, 883-949

### Scrollbar

Pure function. Proportional thumb size and position:

```go
func Scrollbar(height, contentSize, viewportSize, offset int) string {
    thumbSize := max(1, height*viewportSize/contentSize)
    trackSpace := height - thumbSize
    thumbPos := min(trackSpace, offset*trackSpace/(contentSize-viewportSize))
}
```

> crush:internal/ui/common/scrollbar.go:11-46

### Pause Off-Screen Animations

Track visible animations. Pause off-screen ones. Restart after scroll:

```go
func (m *Chat) ScrollByAndAnimate(lines int) tea.Cmd {
    m.list.ScrollBy(lines)
    return m.RestartPausedVisibleAnimations()
}
```

> crush:internal/ui/model/chat.go:170-258

---

## 7. Syntax Highlighting

### Chroma with True-Color

Use `terminal16m` formatter. Three-tier lexer fallback: filename match ->
content analysis -> fallback. Override background per-context:

```go
l := lexers.Match(fileName)
if l == nil { l = lexers.Analyse(source) }
if l == nil { l = lexers.Fallback }
l = chroma.Coalesce(l)
f := formatters.Get("terminal16m")

// Override background for different contexts (diffs, panels):
s, _ = s.Builder().Transform(func(se chroma.StyleEntry) chroma.StyleEntry {
    se.Background = chroma.Colour(bgHex)
    return se
}).Build()
```

> crush:internal/ui/common/highlight.go:10-56

### Glamour Markdown

Configure via `ansi.StyleConfig` structs, not built-in themes. Keep two
configs -- full-color for content, muted for thinking/secondary:

```go
glamour.NewTermRenderer(glamour.WithStyles(styles.Markdown), glamour.WithWordWrap(w))
glamour.NewTermRenderer(glamour.WithStyles(styles.PlainMarkdown), glamour.WithWordWrap(w))
```

> crush:internal/ui/common/markdown.go:10-26

---

## 8. Styling

### Centralized Styles Struct

One struct with nested groups for every component. Construct once, share
via a `Common` pointer. Use semantic color names, not raw hex:

```go
type Styles struct {
    Base, Muted, Subtle    lipgloss.Style
    Primary, Secondary     color.Color
    BgBase, BgSubtle       color.Color
    FgBase, FgMuted        color.Color
    Header  struct { ... }
    Chat    struct { Message struct { ... } }
    Dialog  struct { View lipgloss.Style; ... }
    Markdown, PlainMarkdown ansi.StyleConfig
    Diff    diffview.Style
}
```

> crush:internal/ui/styles/styles.go:60-538

### Gradients

Split into grapheme clusters, blend in HCL space, style individually. Use
sparingly for decorative elements like dialog title lines:

```go
graphemes := splitGraphemes(input)
ramp := blendHCL(c1, c2, len(graphemes))
for i, g := range graphemes { result += style.Foreground(ramp[i]).Render(g) }
```

> crush:internal/ui/styles/grad.go:16-117

### Reusable Elements

Build a library of composable elements: `Section(title, width)` with `─`
fill, `DialogTitle(title, width, c1, c2)` with gradient `╱` fill,
`Status(icon, title, desc, width)` with truncation, `ButtonGroup(buttons)`.

> crush:internal/ui/common/elements.go:17-190

---

## 9. Render Caching

Cache rendered output keyed by width. Invalidate on content change:

```go
func (item *Item) RawRender(width int) string {
    if cached, ok := item.getCached(width); ok { return cached }
    result := item.renderContent(width)
    item.setCache(result, width)
    return result
}
func (item *Item) SetMessage(msg *Message) {
    item.Message = msg
    item.clearCache()
}
```

> crush:internal/ui/chat/assistant.go:81-238

---

## 10. Quick Reference

| Pattern | Rule |
|---------|------|
| Mouse mode | `CellMotion` (drag only), not `AllMotion` |
| Mouse filter | Throttle wheel/motion at 15ms; never filter clicks |
| Mouse routing | Dialog-first, then focus-based, coordinate translation |
| Multi-click | Pending-ID pattern with 400ms threshold |
| Selection | Render callback with `AttrReverse`; UAX#29 for word bounds |
| Dialog stack | Slice-as-stack; front gets messages; all render back-to-front |
| Dialog results | Typed Action structs, not `tea.Cmd`; parent type-switches |
| Dialog render | No backdrop; direct cell overwrite; `CenterRect` geometry |
| Layout | `image.Rectangle` fields; pure layout function; split helpers |
| Responsive | Breakpoint toggle compact/full; adaptive dialog sizing |
| Scrolling | Index + line offset; sticky-bottom; 5 lines/wheel tick |
| Highlighting | Chroma `terminal16m`; 3-tier lexer; bg override per context |
| Markdown | Glamour with custom `ansi.StyleConfig`; two themes |
| Styles | One centralized struct; semantic colors; shared via pointer |
| Caching | Width-keyed render cache; invalidate on mutation |
| Animations | Pre-computed frames; pause off-screen; ID-based tick routing |
| Screen buffer | Draw into rects on shared buffer; dialogs paint last |
