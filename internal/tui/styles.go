package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/xonecas/symb/internal/highlight"
)

// palette and syntaxThemeName are computed once when the TUI is initialised.
var (
	palette         highlight.Palette
	syntaxThemeName string
)

// Semantic color palette — derived from the active Chroma syntax theme.
// Grayscale ramp is a linear interpolation from theme bg to fg.
// Accent is the most saturated token color; error is lerped from the theme Error token.
var (
	ColorHighlight color.Color
	ColorBg        color.Color
	ColorFg        color.Color
	ColorMuted     color.Color
	ColorDim       color.Color
	ColorBorder    color.Color
	ColorSurface   color.Color
	ColorError     color.Color
	ColorWarning   color.Color
	ColorLinkBg    color.Color
)

// initTheme sets the palette and color vars from the given Chroma theme name.
// Must be called before DefaultStyles or any Color* variable is used.
func initTheme(syntaxTheme string) {
	syntaxThemeName = syntaxTheme
	palette = highlight.ThemePalette(syntaxTheme)
	ColorHighlight = lipgloss.Color(palette.Accent)
	ColorBg = lipgloss.Color(palette.Bg)
	ColorFg = lipgloss.Color(palette.Fg)
	ColorMuted = lipgloss.Color(palette.Muted)
	ColorDim = lipgloss.Color(palette.Dim)
	ColorBorder = lipgloss.Color(palette.Border)
	ColorSurface = lipgloss.Color(palette.Accent)
	ColorError = lipgloss.Color(palette.Error)
	ColorWarning = lipgloss.Color(palette.Error) // same source, context differentiates
	ColorLinkBg = lipgloss.Color(palette.LinkBg)
}

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

	// Clickable
	Clickable lipgloss.Style // Clickable text background/foreground

	// Status bar
	StatusText lipgloss.Style // Status bar text
	StatusAdd  lipgloss.Style // Statusbar added count
	StatusMod  lipgloss.Style // Statusbar modified count
	StatusDel  lipgloss.Style // Statusbar removed count

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

		Clickable: lipgloss.NewStyle().Background(ColorLinkBg).Foreground(ColorHighlight),

		StatusText: bg.Foreground(ColorDim),
		StatusAdd:  bg.Foreground(lipgloss.Color("2")),
		StatusMod:  bg.Foreground(lipgloss.Color("3")),
		StatusDel:  bg.Foreground(lipgloss.Color("1")),
	}
}
