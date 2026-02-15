// Package highlight provides syntax highlighting via Chroma, decoupled from any
// specific TUI component.
package highlight

import (
	"fmt"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// Highlight returns an ANSI-highlighted version of text using the given
// Chroma language and theme. bgHex ("#rrggbb") is injected after every ANSI
// reset so the background color is never lost.
func Highlight(text, language, theme, bgHex string) string {
	lex := lexers.Get(language)
	if lex == nil {
		return text
	}
	lex = chroma.Coalesce(lex)
	sty := styles.Get(theme)
	fmtr := formatters.Get("terminal16m")
	if fmtr == nil {
		fmtr = formatters.Fallback
	}
	it, err := lex.Tokenise(nil, text)
	if err != nil {
		return text
	}
	var buf strings.Builder
	if err := fmtr.Format(&buf, sty, it); err != nil {
		return text
	}
	raw := strings.TrimRight(buf.String(), "\n")

	// Chroma's terminal16m formatter skips bg on tokens that inherit from
	// the Background entry, and every \x1b[0m reset clears bg. Fix by
	// replacing resets with reset+bg so the background is always active.
	bgSeq := hexToBgSeq(bgHex)
	return bgSeq + strings.ReplaceAll(raw, "\x1b[0m", "\x1b[0m"+bgSeq)
}

// hexToBgSeq converts "#rrggbb" to an ANSI 24-bit background escape sequence.
func hexToBgSeq(hex string) string {
	if len(hex) != 7 || hex[0] != '#' {
		return ""
	}
	r := hexByte(hex[1], hex[2])
	g := hexByte(hex[3], hex[4])
	b := hexByte(hex[5], hex[6])
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r, g, b)
}

func hexByte(hi, lo byte) int {
	return hexNibble(hi)<<4 | hexNibble(lo)
}

func hexNibble(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return 0
}

// SplitLines splits a highlighted block into per-line strings, propagating
// ANSI style state across lines so each is independently renderable.
func SplitLines(block string) []string {
	lines := strings.Split(block, "\n")
	if len(lines) <= 1 {
		return lines
	}
	var active []string
	for i, line := range lines {
		if i > 0 && len(active) > 0 {
			lines[i] = strings.Join(active, "") + line
		}
		active = scanSGR(line, active)
	}
	return lines
}

// scanSGR scans a line for SGR escape sequences and updates the active
// sequence list. Resets clear the list; other SGRs are appended.
func scanSGR(line string, active []string) []string {
	for j := 0; j < len(line); j++ {
		if line[j] != '\x1b' || j+1 >= len(line) || line[j+1] != '[' {
			continue
		}
		k := j + 2
		for k < len(line) && line[k] != 'm' && line[k] != '\x1b' {
			k++
		}
		if k >= len(line) || line[k] != 'm' {
			continue
		}
		params := line[j+2 : k]
		if params == "" || params == "0" {
			active = active[:0]
		} else {
			active = append(active, line[j:k+1])
		}
		j = k
	}
	return active
}

// ThemeBg extracts the background hex color from a Chroma style.
// Returns "" if no background is set.
func ThemeBg(theme string) string {
	sty := styles.Get(theme)
	if sty == nil {
		return ""
	}
	bg := sty.Get(chroma.Background).Background
	if !bg.IsSet() {
		return ""
	}
	return bg.String() // "#rrggbb"
}

// Palette holds UI chrome colors derived deterministically from a Chroma theme.
// The grayscale ramp is a linear interpolation from bg to fg; the accent is the
// most saturated token color in the palette; error comes from the Error token.
type Palette struct {
	Bg     string // Theme background
	Fg     string // Theme foreground (primary text)
	Border string // 10% bg→fg — borders, dividers
	LinkBg string // 7% bg→fg — subtle hover background
	Dim    string // 25% bg→fg — tertiary text, timestamps
	Muted  string // 45% bg→fg — secondary text, reasoning
	Accent string // Most saturated token color
	Error  string // From chroma Error token, lerped 45% toward fg
}

// ThemePalette derives a full UI color palette from a Chroma theme name.
// Deterministic: same theme → same output. Falls back to sensible defaults
// when the theme is missing entries.
func ThemePalette(theme string) Palette {
	sty := styles.Get(theme)
	if sty == nil {
		return defaultPalette()
	}
	entry := sty.Get(chroma.Background)
	bg := "#000000"
	fg := "#c8c8c8"
	if entry.Background.IsSet() {
		bg = entry.Background.String()
	}
	if entry.Colour.IsSet() {
		fg = entry.Colour.String()
	}

	p := Palette{
		Bg:     bg,
		Fg:     fg,
		Border: lerpHex(bg, fg, 0.10),
		LinkBg: lerpHex(bg, fg, 0.07),
		Dim:    lerpHex(bg, fg, 0.25),
		Muted:  lerpHex(bg, fg, 0.45),
		Accent: pickAccent(sty, fg),
		Error:  pickError(sty, bg, fg),
	}
	return p
}

func defaultPalette() Palette {
	return Palette{
		Bg: "#000000", Fg: "#c8c8c8",
		Border: "#141414", LinkBg: "#0e0e0e",
		Dim: "#323232", Muted: "#5a5a5a",
		Accent: "#00dfff", Error: "#932e2e",
	}
}

// pickAccent returns the most saturated foreground color across all tokens.
func pickAccent(sty *chroma.Style, fallback string) string {
	best := fallback
	bestSat := 0.0
	for tt := chroma.TokenType(0); tt < 2000; tt++ {
		e := sty.Get(tt)
		if !e.Colour.IsSet() {
			continue
		}
		hex := e.Colour.String()
		r, g, b := hexToRGBf(hex)
		mx := maxf(r, maxf(g, b))
		mn := minf(r, minf(g, b))
		if mx == 0 {
			continue
		}
		sat := (mx - mn) / mx
		if sat > bestSat {
			bestSat = sat
			best = hex
		}
	}
	return best
}

// pickError extracts the Error token color and lerps it 45% toward fg
// so it's visible but not garish against the theme background.
func pickError(sty *chroma.Style, bg, fg string) string {
	e := sty.Get(chroma.Error)
	if !e.Colour.IsSet() {
		return lerpHex(bg, fg, 0.45) // muted fallback
	}
	return lerpHex(bg, e.Colour.String(), 0.45)
}

// lerpHex linearly interpolates between two hex colors at fraction t.
func lerpHex(a, b string, t float64) string {
	ar, ag, ab := hexToRGBf(a)
	br, bg, bb := hexToRGBf(b)
	return fmt.Sprintf("#%02x%02x%02x",
		clampByte(ar+(br-ar)*t),
		clampByte(ag+(bg-ag)*t),
		clampByte(ab+(bb-ab)*t),
	)
}

func hexToRGBf(hex string) (float64, float64, float64) {
	if len(hex) != 7 || hex[0] != '#' {
		return 0, 0, 0
	}
	return float64(hexByte(hex[1], hex[2])),
		float64(hexByte(hex[3], hex[4])),
		float64(hexByte(hex[5], hex[6]))
}

func clampByte(v float64) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return int(v + 0.5)
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
