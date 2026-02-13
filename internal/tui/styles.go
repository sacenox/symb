package tui

import "github.com/charmbracelet/lipgloss"

// Semantic color palette — grayscale "suit and tie" with a single accent.
var (
	// Accent — used sparingly: cursor, spinner, active indicators.
	ColorHighlight = lipgloss.Color("#00E5CC")

	// Backgrounds
	ColorBg = lipgloss.Color("#000000") // Pure black — consistent everywhere

	// Foregrounds (grayscale ramp, light to dark)
	ColorFg      = lipgloss.Color("#c8c8c8") // Primary text
	ColorMuted   = lipgloss.Color("#6e6e6e") // Secondary / reasoning
	ColorDim     = lipgloss.Color("#3f3f3f") // Tertiary / timestamps
	ColorBorder  = lipgloss.Color("#1c1c1c") // Borders and dividers
	ColorSurface = ColorHighlight            // Selection highlight — reuse accent

	// Semantic aliases
	ColorError = lipgloss.Color("#932e2e")
)

// Styles holds all pre-built lipgloss styles used across the TUI.
// Constructed once, stored in Model, avoids repeated allocations.
type Styles struct {
	// Text
	Text      lipgloss.Style // Primary text
	Muted     lipgloss.Style // Reasoning, secondary
	Dim       lipgloss.Style // Timestamps, placeholders
	Error     lipgloss.Style // Errors
	ToolCall  lipgloss.Style // Tool call arrows
	ToolArrow lipgloss.Style // Tool arrow symbol

	// Layout
	Border    lipgloss.Style // Divider, separator lines
	Selection lipgloss.Style // Mouse text selection highlight
	BgFill    lipgloss.Style // Pure black background fill for empty areas

	// Status bar
	StatusText lipgloss.Style // Status bar text
}

// DefaultStyles builds the complete style set.
func DefaultStyles() Styles {
	bg := lipgloss.NewStyle().Background(ColorBg)
	return Styles{
		Text:      bg.Foreground(ColorFg),
		Muted:     bg.Foreground(ColorMuted),
		Dim:       bg.Foreground(ColorDim),
		Error:     bg.Foreground(ColorError),
		ToolCall:  bg.Foreground(ColorDim),
		ToolArrow: bg.Foreground(ColorMuted),

		Border:    bg.Foreground(ColorBorder),
		Selection: lipgloss.NewStyle().Background(ColorSurface).Foreground(ColorBg),
		BgFill:    bg,

		StatusText: bg.Foreground(ColorDim),
	}
}
