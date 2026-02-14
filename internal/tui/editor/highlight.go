package editor

import (
	"fmt"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// ---------------------------------------------------------------------------
// Highlight cache (global, shared across instances)
// ---------------------------------------------------------------------------

var (
	hlCache   = make(map[string]string)
	hlCacheMu sync.RWMutex
)

func cachedHighlight(text, language, theme, bgHex string) string {
	cacheKey := language + ":" + theme + ":" + bgHex + ":" + text
	hlCacheMu.RLock()
	if v, ok := hlCache[cacheKey]; ok {
		hlCacheMu.RUnlock()
		return v
	}
	hlCacheMu.RUnlock()

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
	result := bgSeq + strings.ReplaceAll(raw, "\x1b[0m", "\x1b[0m"+bgSeq)

	hlCacheMu.Lock()
	if len(hlCache) > 2000 {
		hlCache = make(map[string]string)
	}
	hlCache[cacheKey] = result
	hlCacheMu.Unlock()
	return result
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

// themeBg extracts the background hex color from a Chroma style.
// Returns "" if no background is set.
func themeBg(theme string) string {
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
