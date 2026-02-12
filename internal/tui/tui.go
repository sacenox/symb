package tui

import (
	"context"
	"image"
	"strings"
	"time"

	"github.com/atotto/clipboard"
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
	inputRows    = 3 // Agent input height
	statusRows   = 2 // Status separator + status bar
	minPaneWidth = 20
)

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

// UpdateToolsMsg is exported so main.go can send it via program.Send.
type UpdateToolsMsg struct{ Tools []mcp.Tool }

// ---------------------------------------------------------------------------
// ELM commands
// ---------------------------------------------------------------------------

func copyToClipboardCmd(text string) tea.Cmd {
	return func() tea.Msg {
		_ = clipboard.WriteAll(text)
		return nil
	}
}

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
	convEntries  []string // Raw styled lines (not wrapped)
	convLines    []string // Wrapped visual lines (cache, rebuilt on width change)
	convCachedW  int      // Width used for current convLines cache
	scrollOffset int      // Lines from bottom (0 = pinned)

	// Mouse state
	resizingPane bool
	selecting    bool
	selectStart  int
	selectEnd    int
}

// New creates a new TUI model.
func New(prov provider.Provider, proxy *mcp.Proxy, tools []mcp.Tool, modelID string) Model {
	sty := DefaultStyles()
	cursorStyle := lipgloss.NewStyle().Foreground(ColorMatrix)

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
		convEntries: []string{},
		updateChan:  ch,
		ctx:         ctx,
		cancel:      cancel,
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

// appendConv appends raw styled entries and returns whether we were at bottom
// (for sticky scroll). Invalidates the wrapped-lines cache.
func (m *Model) appendConv(entries ...string) bool {
	atBottom := m.scrollOffset == 0
	m.convEntries = append(m.convEntries, entries...)
	m.convLines = nil // invalidate cache
	return atBottom
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
	for _, entry := range m.convEntries {
		if entry == "" {
			lines = append(lines, "")
		} else {
			lines = append(lines, wrapANSI(entry, w)...)
		}
	}
	m.convLines = lines
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
		m.appendConv(styledLines(msg.content, m.styles.Text)...)
		m.appendConv("")
		sep := m.makeSeparator("0s", now.Format("15:04:05"))
		wasBottom := m.appendConv(sep)
		m.appendConv("")
		if wasBottom {
			m.scrollOffset = 0
		}
		return m, tea.Batch(m.processLLM(), m.waitForLLMUpdate())

	case llmHistoryMsg:
		m.history = append(m.history, msg.msg)
		return m, m.waitForLLMUpdate()

	case llmAssistantMsg:
		if msg.reasoning != "" {
			wasBottom := m.appendConv(styledLines(msg.reasoning, m.styles.Muted)...)
			m.appendConv("")
			if wasBottom {
				m.scrollOffset = 0
			}
		}
		if msg.content != "" {
			wasBottom := m.appendConv(styledLines(msg.content, m.styles.Text)...)
			m.appendConv("")
			if wasBottom {
				m.scrollOffset = 0
			}
		}
		for _, tc := range msg.toolCalls {
			entry := m.styles.ToolArrow.Render("→") + "  " + m.styles.ToolCall.Render(tc.Name+"(...)")
			wasBottom := m.appendConv(entry)
			if wasBottom {
				m.scrollOffset = 0
			}
		}
		return m, m.waitForLLMUpdate()

	case llmToolResultMsg:
		entry := m.styles.ToolArrow.Render("←") + "  " + m.styles.Dim.Render(msg.content)
		wasBottom := m.appendConv(entry)
		if wasBottom {
			m.scrollOffset = 0
		}
		return m, m.waitForLLMUpdate()

	case llmErrorMsg:
		m.appendConv("", m.styles.Error.Render("Error: "+msg.err.Error()), "")
		return m, nil

	case llmDoneMsg:
		m.appendConv("")
		sep := m.makeSeparator(msg.duration.Round(time.Second).String(), msg.timestamp)
		m.appendConv(sep)
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
			// Calculate which conversation line was hit
			startLine := m.visibleStartLine()
			clickedLine := startLine + (msg.Y - m.layout.conv.Min.Y)

			switch msg.Action {
			case tea.MouseActionPress:
				m.selecting = true
				m.selectStart = clickedLine
				m.selectEnd = clickedLine
			case tea.MouseActionMotion:
				if m.selecting {
					m.selectEnd = clickedLine
				}
			case tea.MouseActionRelease:
				m.selecting = false
				if m.selectStart != m.selectEnd {
					start, end := m.selectStart, m.selectEnd
					if start > end {
						start, end = end, start
					}
					start = max(start, 0)
					end = min(end, totalLines-1)

					var sb strings.Builder
					for i := start; i <= end; i++ {
						sb.WriteString(ansi.Strip(lines[i]))
						if i < end {
							sb.WriteByte('\n')
						}
					}
					cmds = append(cmds, copyToClipboardCmd(sb.String()))
				}
				m.selectStart, m.selectEnd = 0, 0
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

				// Selection highlight
				isSelected := false
				if m.selectStart != m.selectEnd {
					s, e := m.selectStart, m.selectEnd
					if s > e {
						s, e = e, s
					}
					isSelected = lineIdx >= s && lineIdx <= e
				}
				if isSelected {
					line = m.styles.Selection.Render(line)
				}

				lw := lipgloss.Width(line)
				b.WriteString(line)
				if lw < rw {
					pad := strings.Repeat(" ", rw-lw)
					if isSelected {
						pad = m.styles.Selection.Render(pad)
					} else {
						pad = bgFill.Render(pad)
					}
					b.WriteString(pad)
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
