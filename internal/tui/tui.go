package tui

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/xonecas/symb/internal/constants"
	"github.com/xonecas/symb/internal/llm"
	"github.com/xonecas/symb/internal/mcp"
	"github.com/xonecas/symb/internal/mcp_tools"
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

// toolResultFileRe extracts the file path from "Opened path ..." / "Edited path ..." / "Created path ..." headers.
var toolResultFileRe = regexp.MustCompile(`^(?:Opened|Edited|Created)\s+(\S+)`)

// filePathRe matches file references like "path/to/file.go:123" or just "path/to/file.go".
// Requires a '/' to avoid matching version numbers like "v1.0".
var filePathRe = regexp.MustCompile(`(?:^|[\s(])([a-zA-Z0-9_./-]*[/][a-zA-Z0-9_.-]+\.[a-zA-Z]\w*)(?::(\d+))?`)

// generateLayout computes all regions from terminal size and divider position.
func generateLayout(width, height, divX int) layout {
	contentH := height - statusRows
	if contentH < 1 {
		contentH = 1
	}

	// Vertical divider splits left/right at column divX.
	rightX := divX + 1
	rightW := width - rightX
	if rightW < 1 {
		rightW = 1
	}

	// Right pane vertical splits: conv | sep(1) | input(3)
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

// ---------------------------------------------------------------------------
// Mouse filter — throttle high-frequency events at program level.
// ---------------------------------------------------------------------------

var lastMouseEvent time.Time

// MouseEventFilter rate-limits wheel and motion events (15 ms).
// Pass to tea.WithFilter. Never drops clicks.
func MouseEventFilter(_ tea.Model, msg tea.Msg) tea.Msg {
	m, ok := msg.(tea.MouseMsg)
	if !ok {
		return msg
	}
	if m.Button == tea.MouseButtonWheelUp || m.Button == tea.MouseButtonWheelDown ||
		m.Action == tea.MouseActionMotion {
		now := time.Now()
		if now.Sub(lastMouseEvent) < 15*time.Millisecond {
			return nil
		}
		lastMouseEvent = now
	}
	return msg
}

// ---------------------------------------------------------------------------
// Focus
// ---------------------------------------------------------------------------

type focus int

const (
	focusInput  focus = iota // Default: agent input has focus
	focusEditor              // Code viewer has focus
)

// ---------------------------------------------------------------------------
// ELM messages
// ---------------------------------------------------------------------------

type llmUserMsg struct{ content string }

type llmAssistantMsg struct {
	reasoning string
	content   string
	toolCalls []provider.ToolCall
}

type llmToolResultMsg struct {
	toolCallID string
	content    string
}

type llmDoneMsg struct {
	duration  time.Duration
	timestamp string
}

type llmHistoryMsg struct{ msg provider.Message }
type llmErrorMsg struct{ err error }

// Streaming delta messages
type llmContentDeltaMsg struct{ content string }
type llmReasoningDeltaMsg struct{ content string }

// UpdateToolsMsg is exported so main.go can send it via program.Send.
type UpdateToolsMsg struct{ Tools []mcp.Tool }

// ---------------------------------------------------------------------------
// ELM commands
// ---------------------------------------------------------------------------

func (m Model) sendToLLM(userInput string) tea.Cmd {
	return func() tea.Msg { return llmUserMsg{content: userInput} }
}

func (m Model) waitForLLMUpdate() tea.Cmd {
	return func() tea.Msg { return <-m.updateChan }
}

func (m Model) processLLM() tea.Cmd {
	prov := m.provider
	proxy := m.mcpProxy
	tools := make([]mcp.Tool, len(m.mcpTools))
	copy(tools, m.mcpTools)
	history := make([]provider.Message, len(m.history))
	copy(history, m.history)
	ch := m.updateChan
	ctx := m.ctx

	return func() tea.Msg {
		go func() {
			start := time.Now()
			err := llm.ProcessTurn(ctx, llm.ProcessTurnOptions{
				Provider:      prov,
				Proxy:         proxy,
				Tools:         tools,
				History:       history,
				MaxToolRounds: 20,
				OnDelta: func(evt provider.StreamEvent) {
					switch evt.Type {
					case provider.EventContentDelta:
						ch <- llmContentDeltaMsg{content: evt.Content}
					case provider.EventReasoningDelta:
						ch <- llmReasoningDeltaMsg{content: evt.Content}
					}
				},
				OnMessage: func(msg provider.Message) {
					ch <- llmHistoryMsg{msg: msg}
					switch msg.Role {
					case "assistant":
						ch <- llmAssistantMsg{
							reasoning: msg.Reasoning,
							content:   msg.Content,
							toolCalls: msg.ToolCalls,
						}
					case "tool":
						ch <- llmToolResultMsg{
							toolCallID: msg.ToolCallID,
							content:    msg.Content,
						}
					}
				},
			})
			if err != nil {
				ch <- llmErrorMsg{err: err}
				return
			}
			ch <- llmDoneMsg{
				duration:  time.Since(start),
				timestamp: start.Format("15:04"),
			}
		}()
		return nil
	}
}

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

	// Mouse state
	resizingPane bool
}

// New creates a new TUI model.
func New(prov provider.Provider, proxy *mcp.Proxy, tools []mcp.Tool, modelID string) Model {
	sty := DefaultStyles()
	cursorStyle := lipgloss.NewStyle().Foreground(ColorHighlight)

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = cursorStyle.Background(ColorBg)

	ed := editor.New()
	ed.ShowLineNumbers = true
	ed.ReadOnly = true
	ed.Language = "markdown"
	ed.SyntaxTheme = constants.SyntaxTheme
	ed.CursorStyle = cursorStyle
	ed.LineNumStyle = lipgloss.NewStyle().Foreground(ColorBorder)
	ed.BgColor = ColorBg

	ai := editor.New()
	ai.Placeholder = "Type a message..."
	ai.CursorStyle = cursorStyle
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

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// convWidth returns the usable width of the conversation pane.
func (m Model) convWidth() int { return m.layout.conv.Dx() }

// styledLines applies a lipgloss style to each line in a multi-line text.
// No wrapping — lines are stored raw for later wrapping at render time.
func styledLines(text string, style lipgloss.Style) []string {
	raw := strings.Split(text, "\n")
	out := make([]string, len(raw))
	for i, l := range raw {
		out[i] = style.Render(l)
	}
	return out
}

// textEntries converts styled display strings into plain convEntry values.
func textEntries(lines ...string) []convEntry {
	out := make([]convEntry, len(lines))
	for i, l := range lines {
		out[i] = convEntry{display: l, kind: entryText}
	}
	return out
}

// appendConv appends entries and returns whether we were at bottom
// (for sticky scroll). Invalidates the wrapped-lines cache.
func (m *Model) appendConv(entries ...convEntry) bool {
	atBottom := m.scrollOffset == 0
	m.convEntries = append(m.convEntries, entries...)
	m.convLines = nil // invalidate cache
	return atBottom
}

// appendText is a convenience to append plain text entries.
func (m *Model) appendText(lines ...string) bool {
	return m.appendConv(textEntries(lines...)...)
}

// rebuildStreamEntries replaces any existing streaming entries with fresh
// styled entries from the current streamingReasoning and streamingContent.
// Called on each delta to reflect incremental updates.
func (m *Model) rebuildStreamEntries() {
	// Remove old streaming entries
	if m.streamEntryStart >= 0 && m.streamEntryStart <= len(m.convEntries) {
		m.convEntries = m.convEntries[:m.streamEntryStart]
	}

	if m.streamingReasoning != "" {
		m.convEntries = append(m.convEntries, textEntries(styledLines(m.streamingReasoning, m.styles.Muted)...)...)
	}
	if m.streamingContent != "" {
		m.convEntries = append(m.convEntries, textEntries(styledLines(m.streamingContent, m.styles.Text)...)...)
	}
	m.convLines = nil // invalidate cache
}

// wrappedConvLines returns the conversation wrapped to the current convWidth.
// Cached — only recomputed when entries change (nil) or width changes.
func (m *Model) wrappedConvLines() []string {
	w := m.convWidth()
	if m.convLines != nil && m.convCachedW == w {
		return m.convLines
	}
	m.convCachedW = w
	lines := make([]string, 0, len(m.convEntries))
	source := make([]int, 0, len(m.convEntries))
	for i, entry := range m.convEntries {
		if entry.display == "" {
			lines = append(lines, "")
			source = append(source, i)
		} else {
			wrapped := wrapANSI(entry.display, w)
			for range wrapped {
				source = append(source, i)
			}
			lines = append(lines, wrapped...)
		}
	}
	m.convLines = lines
	m.convLineSource = source
	return m.convLines
}

// makeSeparator builds a timestamp separator line.
func (m Model) makeSeparator(dur string, ts string) string {
	label := dur + " " + ts + " "
	fill := m.convWidth() - lipgloss.Width(label)
	if fill < 0 {
		fill = 0
	}
	return m.styles.Dim.Render(label + strings.Repeat("─", fill))
}

// inRect returns true if screen point (x,y) is inside r.
func inRect(x, y int, r image.Rectangle) bool {
	return image.Pt(x, y).In(r)
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	// -- Window resize -------------------------------------------------------
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.divX == 0 {
			m.divX = m.width / 2
		}
		// Constrain divider
		if m.divX < minPaneWidth {
			m.divX = minPaneWidth
		}
		if m.divX > m.width-minPaneWidth {
			m.divX = m.width - minPaneWidth
		}
		m.layout = generateLayout(m.width, m.height, m.divX)
		m.updateComponentSizes()

	// -- Mouse ---------------------------------------------------------------
	case tea.MouseMsg:
		return m.handleMouse(msg)

	// -- Keyboard ------------------------------------------------------------
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.cancel()
			return m, tea.Quit
		case "esc":
			if m.focus == focusInput {
				m.agentInput.Blur()
			} else {
				m.editor.Blur()
			}
			return m, nil
		case "enter":
			if m.focus == focusInput && m.agentInput.Value() != "" {
				userMsg := m.agentInput.Value()
				m.agentInput.Reset()
				return m, m.sendToLLM(userMsg)
			}
		}

	// -- LLM messages --------------------------------------------------------
	case llmUserMsg:
		now := time.Now()
		m.history = append(m.history, provider.Message{
			Role: "user", Content: msg.content, CreatedAt: now,
		})
		m.appendText(styledLines(msg.content, m.styles.Text)...)
		m.appendText("")
		sep := m.makeSeparator("0s", now.Format("15:04:05"))
		wasBottom := m.appendText(sep)
		m.appendText("")
		if wasBottom {
			m.scrollOffset = 0
		}
		return m, tea.Batch(m.processLLM(), m.waitForLLMUpdate())

	case llmReasoningDeltaMsg:
		if !m.streaming {
			m.streaming = true
			m.streamEntryStart = len(m.convEntries)
			m.streamingReasoning = ""
			m.streamingContent = ""
		}
		m.streamingReasoning += msg.content
		m.rebuildStreamEntries()
		if m.scrollOffset == 0 {
			m.scrollOffset = 0 // stay pinned
		}
		return m, m.waitForLLMUpdate()

	case llmContentDeltaMsg:
		if !m.streaming {
			m.streaming = true
			m.streamEntryStart = len(m.convEntries)
			m.streamingReasoning = ""
			m.streamingContent = ""
		}
		m.streamingContent += msg.content
		m.rebuildStreamEntries()
		if m.scrollOffset == 0 {
			m.scrollOffset = 0 // stay pinned
		}
		return m, m.waitForLLMUpdate()

	case llmHistoryMsg:
		m.history = append(m.history, msg.msg)
		return m, m.waitForLLMUpdate()

	case llmAssistantMsg:
		// Finalize streaming state: replace streaming entries with final styled content
		if m.streaming {
			m.streaming = false
			// Remove streaming entries
			if m.streamEntryStart >= 0 && m.streamEntryStart <= len(m.convEntries) {
				m.convEntries = m.convEntries[:m.streamEntryStart]
			}
			m.streamEntryStart = -1
			m.streamingReasoning = ""
			m.streamingContent = ""
			m.convLines = nil // invalidate cache
		}

		if msg.reasoning != "" {
			wasBottom := m.appendText(styledLines(msg.reasoning, m.styles.Muted)...)
			m.appendText("")
			if wasBottom {
				m.scrollOffset = 0
			}
		}
		if msg.content != "" {
			wasBottom := m.appendText(styledLines(msg.content, m.styles.Text)...)
			m.appendText("")
			if wasBottom {
				m.scrollOffset = 0
			}
		}
		for _, tc := range msg.toolCalls {
			entry := m.styles.ToolArrow.Render("→") + "  " + m.styles.ToolCall.Render(tc.Name+"(...)")
			wasBottom := m.appendText(entry)
			if wasBottom {
				m.scrollOffset = 0
			}
		}
		return m, m.waitForLLMUpdate()

	case llmToolResultMsg:
		// Extract file path from tool result header (Opened/Edited/Created)
		var filePath string
		if sm := toolResultFileRe.FindStringSubmatch(msg.content); sm != nil {
			filePath = sm[1]
		}

		lines := strings.Split(msg.content, "\n")
		preview := lines
		truncated := false
		if len(lines) > maxPreviewLines {
			preview = lines[:maxPreviewLines]
			truncated = true
		}

		arrow := m.styles.ToolArrow.Render("←") + "  "
		var wasBottom bool
		for i, line := range preview {
			display := m.styles.Dim.Render(line)
			if i == 0 {
				display = arrow + display
				wasBottom = m.appendConv(convEntry{display: display, kind: entryToolResult, filePath: filePath, full: msg.content})
			} else {
				m.appendConv(convEntry{display: display, kind: entryToolResult, filePath: filePath, full: msg.content})
			}
		}
		if truncated {
			hint := fmt.Sprintf("  ... %d more lines (click to view)", len(lines)-maxPreviewLines)
			m.appendConv(convEntry{display: m.styles.Muted.Render(hint), kind: entryToolResult, filePath: filePath, full: msg.content})
		}
		if wasBottom {
			m.scrollOffset = 0
		}
		return m, m.waitForLLMUpdate()

	case llmErrorMsg:
		m.appendText("", m.styles.Error.Render("Error: "+msg.err.Error()), "")
		return m, nil

	case llmDoneMsg:
		m.appendText("")
		sep := m.makeSeparator(msg.duration.Round(time.Second).String(), msg.timestamp)
		m.appendText(sep)
		return m, nil

	case mcp_tools.OpenForUserMsg:
		m.editor.SetValue(msg.Content)
		m.editor.Language = msg.Language
		m.focus = focusEditor
		m.agentInput.Blur()
		m.editor.Focus()
		return m, nil

	case UpdateToolsMsg:
		m.mcpTools = msg.Tools
		return m, nil
	}

	// Always tick spinner
	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	cmds = append(cmds, cmd)

	// Forward non-mouse messages to focused component
	if _, isMouse := msg.(tea.MouseMsg); !isMouse {
		m.editor, cmd = m.editor.Update(msg)
		cmds = append(cmds, cmd)
		m.agentInput, cmd = m.agentInput.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

// updateComponentSizes pushes layout dimensions to sub-models.
func (m *Model) updateComponentSizes() {
	m.editor.SetWidth(m.layout.editor.Dx())
	m.editor.SetHeight(m.layout.editor.Dy())
	m.agentInput.SetWidth(m.layout.input.Dx() - 2) // padding for border
	m.agentInput.SetHeight(inputRows)
}

// ---------------------------------------------------------------------------
// Mouse handling — dialog-first when we add dialogs, then focus-based.
// Coordinate translation via layout rects.
// ---------------------------------------------------------------------------

func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	// --- Divider drag -------------------------------------------------------
	if msg.Button == tea.MouseButtonLeft && inRect(msg.X, msg.Y, m.layout.div) {
		if msg.Action == tea.MouseActionPress {
			m.resizingPane = true
		}
	}
	if msg.Action == tea.MouseActionRelease {
		m.resizingPane = false
	}
	if m.resizingPane && msg.Action == tea.MouseActionMotion {
		newDiv := msg.X
		if newDiv >= minPaneWidth && newDiv <= m.width-minPaneWidth {
			m.divX = newDiv
			m.layout = generateLayout(m.width, m.height, m.divX)
			m.updateComponentSizes()
		}
		return m, nil
	}

	// --- Focus switching on click -------------------------------------------
	if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
		switch {
		case inRect(msg.X, msg.Y, m.layout.editor):
			m.focus = focusEditor
			m.agentInput.Blur()
			m.editor.Focus()
		case inRect(msg.X, msg.Y, m.layout.input):
			m.focus = focusInput
			m.editor.Blur()
			m.agentInput.Focus()
		}
	}

	// --- Editor: forward with original coords (left pane starts at 0) -------
	if inRect(msg.X, msg.Y, m.layout.editor) {
		var cmd tea.Cmd
		m.editor, cmd = m.editor.Update(msg)
		cmds = append(cmds, cmd)
		return m, tea.Batch(cmds...)
	}

	// --- Input: translate coords to component-local -------------------------
	if inRect(msg.X, msg.Y, m.layout.input) {
		translated := msg
		translated.X = msg.X - m.layout.input.Min.X
		translated.Y = msg.Y - m.layout.input.Min.Y
		var cmd tea.Cmd
		m.agentInput, cmd = m.agentInput.Update(translated)
		cmds = append(cmds, cmd)
		return m, tea.Batch(cmds...)
	}

	// --- Conversation: scroll + selection -----------------------------------
	if inRect(msg.X, msg.Y, m.layout.conv) {
		convH := m.layout.conv.Dy()
		lines := m.wrappedConvLines()
		totalLines := len(lines)

		switch msg.Button {
		case tea.MouseButtonLeft:
			if msg.Action == tea.MouseActionRelease {
				// Single click — try to handle as interactive click
				startLine := m.visibleStartLine()
				clickedLine := startLine + (msg.Y - m.layout.conv.Min.Y)
				if cmd := m.handleConvClick(clickedLine); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}

		case tea.MouseButtonWheelUp:
			maxScroll := totalLines - convH
			if maxScroll < 0 {
				maxScroll = 0
			}
			m.scrollOffset = min(m.scrollOffset+5, maxScroll)

		case tea.MouseButtonWheelDown:
			m.scrollOffset = max(m.scrollOffset-5, 0)
		}
	}

	return m, tea.Batch(cmds...)
}

// visibleStartLine returns the index of the first visible wrapped conversation line.
func (m *Model) visibleStartLine() int {
	lines := m.wrappedConvLines()
	total := len(lines)
	visible := m.layout.conv.Dy()
	if total <= visible {
		return 0
	}
	start := total - visible - m.scrollOffset
	if start < 0 {
		return 0
	}
	return start
}

// handleConvClick resolves a click on a wrapped conversation line.
// If the line belongs to a tool result entry, the full content is opened in
// the editor. Otherwise, if the line contains a file path reference
// (path/to/file.go:123), that file is opened in the editor.
func (m *Model) handleConvClick(wrappedLine int) tea.Cmd {
	m.wrappedConvLines() // ensure convLineSource is fresh
	src := m.convLineSource
	if wrappedLine < 0 || wrappedLine >= len(src) {
		return nil
	}
	entryIdx := src[wrappedLine]
	if entryIdx < 0 || entryIdx >= len(m.convEntries) {
		return nil
	}
	entry := m.convEntries[entryIdx]

	// Tool result: open the source file or fall back to raw content
	if entry.kind == entryToolResult {
		if entry.filePath != "" {
			if content, err := os.ReadFile(entry.filePath); err == nil {
				m.editor.SetValue(string(content))
				m.editor.Language = mcp_tools.DetectLanguage(entry.filePath)
				m.focus = focusEditor
				m.agentInput.Blur()
				m.editor.Focus()
				return nil
			}
		}
		// Fallback: show raw tool result text
		if entry.full != "" {
			m.editor.SetValue(entry.full)
			m.editor.Language = "text"
			m.focus = focusEditor
			m.agentInput.Blur()
			m.editor.Focus()
			return nil
		}
	}

	// Try to extract a file path from the clicked line's plain text
	lines := m.wrappedConvLines()
	if wrappedLine >= len(lines) {
		return nil
	}
	plain := ansi.Strip(lines[wrappedLine])
	return m.tryOpenFilePath(plain)
}

// tryOpenFilePath looks for a file:line reference in text and opens it in the editor.
func (m *Model) tryOpenFilePath(text string) tea.Cmd {
	matches := filePathRe.FindStringSubmatch(text)
	if matches == nil {
		return nil
	}
	path := matches[1]
	lineNum := 0
	if matches[2] != "" {
		lineNum, _ = strconv.Atoi(matches[2])
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil
	}

	// Restrict to files within the working directory
	wd, err := os.Getwd()
	if err != nil {
		return nil
	}
	rel, err := filepath.Rel(wd, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return nil
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return nil
	}

	language := mcp_tools.DetectLanguage(path)
	m.editor.SetValue(string(content))
	m.editor.Language = language
	if lineNum > 0 {
		m.editor.GotoLine(lineNum)
	}
	m.focus = focusEditor
	m.agentInput.Blur()
	m.editor.Focus()
	return nil
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

func (m Model) View() string {
	if m.width == 0 {
		return ""
	}

	ly := m.layout
	contentH := m.height - statusRows
	var b strings.Builder

	// Pre-split editor and input views
	editorLines := strings.Split(m.editor.View(), "\n")
	inputLines := strings.Split(m.agentInput.View(), "\n")

	// Conversation visible window (wrapped to current width)
	convLines := m.wrappedConvLines()
	startLine := m.visibleStartLine()

	bgFill := m.styles.BgFill

	for row := 0; row < contentH; row++ {
		// -- Left pane: editor -----------------------------------------------
		edW := ly.editor.Dx()
		if row < len(editorLines) {
			line := editorLines[row]
			lw := lipgloss.Width(line)
			if lw > edW {
				line = ansi.Truncate(line, edW, "")
				lw = lipgloss.Width(line)
			}
			b.WriteString(line)
			if lw < edW {
				b.WriteString(bgFill.Render(strings.Repeat(" ", edW-lw)))
			}
		} else {
			b.WriteString(bgFill.Render(strings.Repeat(" ", edW)))
		}

		// -- Divider ---------------------------------------------------------
		b.WriteString(m.styles.Border.Render("│"))

		// -- Right pane ------------------------------------------------------
		rw := m.convWidth()
		relY := row // row relative to right pane top

		if relY < ly.conv.Dy() {
			// Conversation area
			lineIdx := startLine + relY
			if lineIdx < len(convLines) {
				line := convLines[lineIdx]
				lw := lipgloss.Width(line)
				b.WriteString(line)
				if lw < rw {
					b.WriteString(bgFill.Render(strings.Repeat(" ", rw-lw)))
				}
			} else {
				b.WriteString(bgFill.Render(strings.Repeat(" ", rw)))
			}

		} else if relY == ly.sep.Min.Y {
			// Separator line between conversation and input
			b.WriteString(m.styles.Border.Render(strings.Repeat("─", rw)))

		} else {
			// Input area
			inputRow := relY - ly.input.Min.Y
			if inputRow >= 0 && inputRow < len(inputLines) {
				line := inputLines[inputRow]
				lw := lipgloss.Width(line)
				if lw > rw {
					line = ansi.Truncate(line, rw, "")
					lw = lipgloss.Width(line)
				}
				b.WriteString(line)
				if lw < rw {
					b.WriteString(bgFill.Render(strings.Repeat(" ", rw-lw)))
				}
			} else {
				b.WriteString(bgFill.Render(strings.Repeat(" ", rw)))
			}
		}

		b.WriteByte('\n')
	}

	// -- Status separator: ───┴─── ------------------------------------------
	divX := ly.div.Min.X
	b.WriteString(m.styles.Border.Render(strings.Repeat("─", divX)))
	b.WriteString(m.styles.Border.Render("┴"))
	b.WriteString(m.styles.Border.Render(strings.Repeat("─", m.width-divX-1)))
	b.WriteByte('\n')

	// -- Status bar ----------------------------------------------------------
	left := m.styles.StatusText.Render(" symb")
	spin := strings.TrimSpace(m.spinner.View())
	leftW := lipgloss.Width(left)
	spinW := lipgloss.Width(spin)
	gap := m.width - leftW - spinW - 1
	if gap < 0 {
		gap = 0
	}
	b.WriteString(left)
	b.WriteString(bgFill.Render(strings.Repeat(" ", gap)))
	b.WriteString(spin)
	b.WriteString(bgFill.Render(" "))

	return b.String()
}
