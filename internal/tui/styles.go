package tui

import (
	"charm.land/lipgloss/v2"
	"github.com/xonecas/symb/internal/constants"
	"github.com/xonecas/symb/internal/highlight"
)

// palette is computed once from the Chroma theme at init time.
var palette = highlight.ThemePalette(constants.SyntaxTheme)

// Semantic color palette — derived from the active Chroma syntax theme.
// Grayscale ramp is a linear interpolation from theme bg to fg.
// Accent is the most saturated token color; error is lerped from the theme Error token.
var (
	ColorHighlight = lipgloss.Color(palette.Accent)
	ColorBg        = lipgloss.Color(palette.Bg)
	ColorFg        = lipgloss.Color(palette.Fg)
	ColorMuted     = lipgloss.Color(palette.Muted)
	ColorDim       = lipgloss.Color(palette.Dim)
	ColorBorder    = lipgloss.Color(palette.Border)
	ColorSurface   = lipgloss.Color(palette.Accent)
	ColorError     = lipgloss.Color(palette.Error)
	ColorWarning   = lipgloss.Color(palette.Error) // same source, context differentiates
	ColorLinkBg    = lipgloss.Color(palette.LinkBg)
)

// Styles holds all pre-built lipgloss styles used across the TUI.
// Constructed once, stored in Model, avoids repeated allocations.
type Styles struct {
	// Text
	Text      lipgloss.Style // Primary text
	Muted     lipgloss.Style // Reasoning, secondary
	Dim       lipgloss.Style // Timestamps, placeholders
	Error     lipgloss.Style // Errors
	Warning   lipgloss.Style // Warnings
	ToolCall  lipgloss.Style // Tool call arrows
	ToolArrow lipgloss.Style // Tool arrow symbol

	// Layout
	Border    lipgloss.Style // Divider, separator lines
	Selection lipgloss.Style // Mouse text selection highlight
	BgFill    lipgloss.Style // Background fill for empty areas

	// Hover
	Hover lipgloss.Style // Highlight text on subtle dark bg for clickable hover

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
		Warning:   bg.Foreground(ColorWarning),
		ToolCall:  bg.Foreground(ColorDim),
		ToolArrow: bg.Foreground(ColorMuted),

		Border:    bg.Foreground(ColorBorder),
		Selection: lipgloss.NewStyle().Background(ColorSurface).Foreground(ColorBg),
		BgFill:    bg,

		Hover: lipgloss.NewStyle().Background(ColorLinkBg).Foreground(ColorHighlight),

		StatusText: bg.Foreground(ColorDim),
	}
}
