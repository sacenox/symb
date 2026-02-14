package tui

import (
	"context"
	"image"
	"regexp"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/xonecas/symb/internal/constants"
	"github.com/xonecas/symb/internal/llm"
	"github.com/xonecas/symb/internal/mcp"
	"github.com/xonecas/symb/internal/provider"
	"github.com/xonecas/symb/internal/tui/editor"
)

// ---------------------------------------------------------------------------
// Layout
// ---------------------------------------------------------------------------

// layout holds computed rectangles for every TUI region.
// Recomputed from terminal dimensions on every resize.
type layout struct {
	editor image.Rectangle // Left pane: code viewer
	conv   image.Rectangle // Right pane: conversation log
	sep    image.Rectangle // Right pane: separator between conv and input
	input  image.Rectangle // Right pane: agent input
	div    image.Rectangle // Vertical divider column (1-wide)
}

const (
	inputRows       = 3 // Agent input height
	statusRows      = 2 // Status separator + status bar
	minPaneWidth    = 20
	maxPreviewLines = 5 // Max lines shown for tool results before truncation
)

// generateLayout computes all regions from terminal size and divider position.
func generateLayout(width, height, divX int) layout {
	contentH := height - statusRows
	if contentH < 1 {
		contentH = 1
	}

	rightX := divX + 1
	rightW := width - rightX
	if rightW < 1 {
		rightW = 1
	}

	sepY := contentH - inputRows - 1
	if sepY < 0 {
		sepY = 0
	}
	inputY := contentH - inputRows
	if inputY < 0 {
		inputY = 0
	}

	return layout{
		editor: image.Rect(0, 0, divX, contentH),
		div:    image.Rect(divX, 0, divX+1, contentH),
		conv:   image.Rect(rightX, 0, rightX+rightW, sepY),
		sep:    image.Rect(rightX, sepY, rightX+rightW, sepY+1),
		input:  image.Rect(rightX, inputY, rightX+rightW, inputY+inputRows),
	}
}

// inRect returns true if screen point (x,y) is inside r.
func inRect(x, y int, r image.Rectangle) bool {
	return image.Pt(x, y).In(r)
}

// ---------------------------------------------------------------------------
// Focus
// ---------------------------------------------------------------------------

type focus int

const (
	focusInput  focus = iota // Default: agent input has focus
	focusEditor              // Code viewer has focus
)

// setFocus switches focus between editor and input panes.
func (m *Model) setFocus(f focus) {
	m.focus = f
	switch f {
	case focusEditor:
		m.agentInput.Blur()
		m.editor.Focus()
	case focusInput:
		m.editor.Blur()
		m.agentInput.Focus()
	}
}

// ---------------------------------------------------------------------------
// Conversation types
// ---------------------------------------------------------------------------

// entryKind distinguishes conversation entry types for click handling.
type entryKind int

const (
	entryText       entryKind = iota // Plain text (user, assistant, separator)
	entryToolResult                  // Tool result — clickable to view full content in editor
)

// convEntry is a single logical entry in the conversation pane.
type convEntry struct {
	display  string    // Styled text for rendering (may be truncated for tool results)
	kind     entryKind // Entry type
	filePath string    // Source file path (for tool results that reference a file)
	full     string    // Fallback raw content (when no file path, e.g. Grep results)
}

// toolResultFileRe extracts the file path from "Read path ..." / "Edited path ..." / "Created path ..." headers.
var toolResultFileRe = regexp.MustCompile(`^(?:Read|Edited|Created)\s+(\S+)`)

// filePathRe matches file references like "path/to/file.go:123" or just "path/to/file.go".
// Requires a '/' to avoid matching version numbers like "v1.0".
var filePathRe = regexp.MustCompile(`(?:^|[\s(])([a-zA-Z0-9_./-]*[/][a-zA-Z0-9_.-]+\.[a-zA-Z]\w*)(?::(\d+))?`)

// ---------------------------------------------------------------------------
// Conversation selection (character-level)
// ---------------------------------------------------------------------------

type convPos struct{ line, col int }

// convSelection tracks a character-level selection in the conversation pane.
type convSelection struct {
	anchor convPos // Where the selection started
	active convPos // Current selection endpoint
}

func (s convSelection) ordered() (start, end convPos) {
	if s.anchor.line > s.active.line ||
		(s.anchor.line == s.active.line && s.anchor.col > s.active.col) {
		return s.active, s.anchor
	}
	return s.anchor, s.active
}

func (s convSelection) empty() bool { return s.anchor == s.active }

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

// Model is the top-level TUI model.
type Model struct {
	// Terminal dimensions
	width, height int

	// Sub-models
	spinner    spinner.Model
	editor     editor.Model
	agentInput editor.Model

	// Layout
	layout layout
	divX   int // Divider X position (resizable)
	focus  focus
	styles Styles

	// LLM
	provider   provider.Provider
	mcpProxy   *mcp.Proxy
	mcpTools   []mcp.Tool
	history    []provider.Message
	updateChan chan tea.Msg
	ctx        context.Context
	cancel     context.CancelFunc

	// Conversation
	convEntries    []convEntry // Conversation entries (not wrapped)
	convLines      []string    // Wrapped visual lines (cache, rebuilt on width change)
	convLineSource []int       // Maps each wrapped line -> index in convEntries
	convCachedW    int         // Width used for current convLines cache
	scrollOffset   int         // Lines from bottom (0 = pinned)

	// Streaming state: raw text accumulated during streaming, styled at render time
	streamingReasoning string // In-progress reasoning text
	streamingContent   string // In-progress content text
	streaming          bool   // Whether we're currently streaming
	streamEntryStart   int    // Index in convEntries where streaming entries begin (-1 = none)

	// Editor state
	editorFilePath string // absolute path of the file currently shown in the editor

	// Mouse state
	resizingPane bool

	// Conversation selection
	convSel      *convSelection
	convDragging bool
}

// New creates a new TUI model.
func New(prov provider.Provider, proxy *mcp.Proxy, tools []mcp.Tool, modelID string) Model {
	sty := DefaultStyles()
	cursorStyle := lipgloss.NewStyle().Foreground(ColorHighlight)

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = cursorStyle.Background(ColorBg)

	selStyle := sty.Selection

	ed := editor.New()
	ed.ShowLineNumbers = true
	ed.ReadOnly = true
	ed.Language = "markdown"
	ed.SyntaxTheme = constants.SyntaxTheme
	ed.CursorStyle = cursorStyle
	ed.SelectionStyle = selStyle
	ed.LineNumStyle = lipgloss.NewStyle().Foreground(ColorBorder)
	ed.BgColor = ColorBg
	ed.MarkAddStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#4ec964")).Background(ColorBg)
	ed.MarkChgStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#e5c07b")).Background(ColorBg)
	ed.MarkDelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#e06c75")).Background(ColorBg)
	ed.DiagErrStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#e06c75"))
	ed.DiagWarnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#e5c07b"))

	ai := editor.New()
	ai.Placeholder = "Type a message..."
	ai.CursorStyle = cursorStyle
	ai.SelectionStyle = selStyle
	ai.PlaceholderSty = lipgloss.NewStyle().Foreground(ColorDim).Background(ColorBg)
	ai.BgColor = ColorBg
	ai.Focus()

	ch := make(chan tea.Msg, 500)
	ctx, cancel := context.WithCancel(context.Background())

	systemPrompt := llm.BuildSystemPrompt(modelID)

	return Model{
		spinner:    s,
		editor:     ed,
		agentInput: ai,
		styles:     sty,
		focus:      focusInput,

		provider:    prov,
		mcpProxy:    proxy,
		mcpTools:    tools,
		history:     []provider.Message{{Role: "system", Content: systemPrompt, CreatedAt: time.Now()}},
		convEntries: []convEntry{},
		updateChan:  ch,
		ctx:         ctx,
		cancel:      cancel,

		streamEntryStart: -1,
	}
}

// Init starts spinner and cursor blink.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, func() tea.Msg { return editor.Blink() })
}
