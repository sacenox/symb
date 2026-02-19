package tui

import (
	"context"
	"image"
	"regexp"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/xonecas/symb/internal/delta"
	"github.com/xonecas/symb/internal/filesearch"
	"github.com/xonecas/symb/internal/llm"
	"github.com/xonecas/symb/internal/mcp"
	"github.com/xonecas/symb/internal/provider"
	"github.com/xonecas/symb/internal/store"
	"github.com/xonecas/symb/internal/treesitter"
	"github.com/xonecas/symb/internal/tui/editor"
	"github.com/xonecas/symb/internal/tui/modal"
)

// ---------------------------------------------------------------------------
// Layout
// ---------------------------------------------------------------------------

// layout holds computed rectangles for every TUI region.
// Recomputed from terminal dimensions on every resize.
type layout struct {
	conv  image.Rectangle // Conversation log
	sep   image.Rectangle // Separator between conv and input
	input image.Rectangle // Agent input
}

const (
	inputRows       = 3 // Agent input height
	statusRows      = 2 // Status separator + status bar
	maxDisplayTurns = 5 // Max conversation turns kept in memory; older turns live in DB

	roleAssistant = "assistant"
)

// generateLayout computes all regions from terminal size.
func generateLayout(width, height int) layout {
	contentH := height - statusRows
	if contentH < 1 {
		contentH = 1
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
		conv:  image.Rect(0, 0, width, sepY),
		sep:   image.Rect(0, sepY, width, sepY+1),
		input: image.Rect(0, inputY, width, inputY+inputRows),
	}
}

// inRect returns true if screen point (x,y) is inside r.
func inRect(x, y int, r image.Rectangle) bool {
	return image.Pt(x, y).In(r)
}

// ---------------------------------------------------------------------------
// Conversation types
// ---------------------------------------------------------------------------

// entryKind distinguishes conversation entry types for click handling.
type entryKind int

const (
	entryText       entryKind = iota // Plain text (user, assistant, reasoning)
	entryToolCall                    // Tool call arrow line (→ ToolName(...))
	entryToolResult                  // Tool result summary (← summary [view])
	entryToolDiag                    // Tool diagnostics — non-clickable
	entryUndo                        // Undo button — small clickable label
	entrySeparator                   // Turn-end separator (timestamp + tokens)
)

// convEntry is a single logical entry in the conversation pane.
type convEntry struct {
	display  string    // Styled text for rendering (may be truncated for tool results)
	kind     entryKind // Entry type
	filePath string    // Source file path (for tool results that reference a file)
	full     string    // Full raw content (for editor viewing or undo separator restore)
	line     int       // Target line (1-indexed) for cursor positioning on click (0 = none)
	toolName string    // Tool name for view button context (Read, Edit, Shell, etc.)
}

// toolResultFileRe extracts the file path from "Read path ..." / "Edited path ..." / "Created path ..." headers.
var toolResultFileRe = regexp.MustCompile(`^(?:Read|Edited|Created)\s+(\S+)`)

// toolResultLineRe extracts the start line from "(lines N-M)" in tool result headers.
var toolResultLineRe = regexp.MustCompile(`\(lines\s+(\d+)-\d+\)`)

// toolResultRangeRe extracts both start and end lines from "(lines N-M)".
var toolResultRangeRe = regexp.MustCompile(`\(lines\s+(\d+)-(\d+)\)`)

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

// FileReadResetter allows clearing the file read tracker on undo.
type FileReadResetter interface {
	Reset()
}

// ---------------------------------------------------------------------------
// Turn tracking (for undo)
// ---------------------------------------------------------------------------

// turnBoundary marks the start of a user turn in convEntries.
type turnBoundary struct {
	convIdx      int   // index in m.convEntries where this turn's display starts
	dbMsgID      int64 // messages.id of the user message (for DB cleanup)
	inputTokens  int   // total input tokens at start of this turn
	outputTokens int   // total output tokens at start of this turn
}

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

// Model is the top-level TUI model.
type Model struct {
	// Terminal dimensions
	width, height int

	// Sub-models
	agentInput editor.Model

	// Layout
	layout layout
	styles Styles

	// LLM
	provider   provider.Provider
	mcpProxy   *mcp.Proxy
	mcpTools   []mcp.Tool
	updateChan chan tea.Msg
	ctx        context.Context
	cancel     context.CancelFunc
	turnCtx    context.Context    // per-turn child context (nil when idle)
	turnCancel context.CancelFunc // cancels current LLM turn only (nil when idle)

	// Session persistence
	store            *store.Cache
	storeQueue       chan storeBatch
	storeQueueDone   <-chan struct{}
	sessionID        string
	initialSystemMsg *provider.Message

	// Conversation
	convEntries    []convEntry // Conversation entries (not wrapped)
	convLineSource []int       // Maps each wrapped line -> index in convEntries (recomputed each frame)
	scrollOffset   int         // Lines from bottom (0 = pinned)

	// Streaming state: raw text accumulated during streaming, styled at render time
	streamingReasoning string // In-progress reasoning text
	streamingContent   string // In-progress content text
	streaming          bool   // Whether we're currently streaming
	streamEntryStart   int    // Index in convEntries where streaming entries begin (-1 = none)

	// Token usage tracking
	turnInputTokens   int // accumulated input tokens for current turn
	turnOutputTokens  int // accumulated output tokens for current turn
	turnContextTokens int // context size (last API call's input tokens)
	totalInputTokens  int // session-wide total input tokens
	totalOutputTokens int // session-wide total output tokens

	// Context recitation
	scratchpad llm.ScratchpadReader // agent plan injected at context tail

	// Undo
	deltaTracker   *delta.Tracker
	turnBoundaries []turnBoundary
	fileTracker    FileReadResetter // for clearing read-tracking on undo
	tsIndex        *treesitter.Index

	// File finder modal
	fileModal *modal.Model
	atOffset  int // rune offset where @ was typed (for file modal replacement)
	// Keybinds modal
	keybindsModal *modal.Model
	// Models modal
	modelsModal *modal.Model
	// Tool viewer modal
	toolViewModal *modal.ToolView
	searcher      *filesearch.Searcher

	// Provider switching
	registry         *provider.Registry
	providerOpts     provider.Options
	currentModelName string
	cachedModels     []provider.TaggedModel // cached across all providers
	sharedProvider   *atomic.Pointer[provider.Provider]
	// Pending tool calls: maps tool call ID → arguments for line extraction
	pendingToolCalls map[string]provider.ToolCall

	// Conversation selection
	convSel      *convSelection
	convDragging bool

	// Frame loop
	streamDirty  bool     // New streaming content arrived since last rebuild
	frameLines   []string // Per-frame cache of wrapped conv lines (cleared each Update)
	turnPending  bool     // True while awaiting user message persistence
	undoInFlight bool     // True while undo side-effects are running

	// Statusbar state
	providerConfigName string // TOML config key (e.g. "zen-pickle")
	gitBranch          string // Current git branch name
	gitDirty           bool   // Working tree has uncommitted changes
	gitAdded           int
	gitModified        int
	gitRemoved         int

	lastNetError string // Last LLM network error (truncated for display)
	llmInFlight  bool   // True while an LLM turn is in progress

	// Statusbar animation
	spinFrame   int       // Current braille spinner frame index
	spinFrameAt time.Time // When the current frame was set
}

// New creates a new TUI model.
// If resumeHistory is non-nil, the session is being resumed and messages are
// loaded from the database instead of creating a fresh system prompt.
func New(prov provider.Provider, sharedProvider *atomic.Pointer[provider.Provider], proxy *mcp.Proxy, tools []mcp.Tool, modelID string, db *store.Cache, sessionID string, idx *treesitter.Index, dt *delta.Tracker, ft FileReadResetter, providerConfigName string, pad llm.ScratchpadReader, resumeHistory []provider.Message, registry *provider.Registry, providerOpts provider.Options, syntaxTheme string) Model {
	initTheme(syntaxTheme)
	sty := DefaultStyles()
	cursorStyle := lipgloss.NewStyle().Foreground(ColorHighlight)

	selStyle := sty.Selection

	ai := editor.New()
	ai.Placeholder = "Ask anything... (CTRL+h for keybinds)"
	ai.SubmitOnEnter = true
	ai.Language = "markdown"
	ai.SyntaxTheme = syntaxTheme
	ai.CursorStyle = cursorStyle
	ai.SelectionStyle = selStyle
	ai.PlaceholderSty = lipgloss.NewStyle().Foreground(ColorDim).Background(ColorBg)
	ai.BgColor = ColorBg
	ai.Focus()

	ch := make(chan tea.Msg, 500)
	var storeQueue chan storeBatch
	var storeQueueDone <-chan struct{}
	if db != nil {
		storeQueue = make(chan storeBatch, 256)
		storeQueueDone = startStoreWorker(db, storeQueue)
	}
	ctx, cancel := context.WithCancel(context.Background())

	var entries []convEntry
	var initialSystemMsg *provider.Message
	if resumeHistory != nil {
		entries = historyConvEntries(resumeHistory, sty)
	} else {
		systemPrompt := llm.BuildSystemPrompt(modelID, idx)
		systemMsg := provider.Message{Role: "system", Content: systemPrompt, CreatedAt: time.Now()}
		initialSystemMsg = &systemMsg
	}

	return Model{
		agentInput: ai,
		styles:     sty,

		provider:    prov,
		mcpProxy:    proxy,
		mcpTools:    tools,
		convEntries: entries,
		updateChan:  ch,
		ctx:         ctx,
		cancel:      cancel,

		store:            db,
		storeQueue:       storeQueue,
		storeQueueDone:   storeQueueDone,
		sessionID:        sessionID,
		initialSystemMsg: initialSystemMsg,

		scratchpad:   pad,
		deltaTracker: dt,
		fileTracker:  ft,
		tsIndex:      idx,

		searcher:         newSearcherOrNil("."),
		registry:         registry,
		providerOpts:     providerOpts,
		currentModelName: modelID,
		sharedProvider:   sharedProvider,

		streamEntryStart: -1,

		providerConfigName: providerConfigName,
	}
}

func newSearcherOrNil(root string) *filesearch.Searcher {
	s, err := filesearch.NewSearcher(root)
	if err != nil {
		return nil
	}
	return s
}

// Init starts the 60fps frame loop and periodic git branch polling.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{frameTick(), gitBranchCmd()}
	if m.initialSystemMsg != nil {
		cmds = append(cmds, m.saveMessagesCmd([]provider.Message{*m.initialSystemMsg}))
	}
	return tea.Batch(cmds...)
}
