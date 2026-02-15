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
