package tui

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/xonecas/symb/internal/hashline"
)

// atMentionRe matches @path tokens, where path is any non-whitespace sequence.
var atMentionRe = regexp.MustCompile(`@(\S+)`)

// expandAtMentions replaces @path tokens in the input string with the
// hashline-tagged contents of the referenced file. If the file cannot be
// read, the token is left as-is.
func expandAtMentions(input string) string {
	return atMentionRe.ReplaceAllStringFunc(input, func(match string) string {
		path := match[1:] // strip leading @
		data, err := os.ReadFile(path)
		if err != nil {
			return match // leave token intact
		}
		content := strings.TrimRight(string(data), "\n")
		tagged := hashline.TagLines(content, 1)
		var sb strings.Builder
		fmt.Fprintf(&sb, "@%s\n", path)
		sb.WriteString(hashline.FormatTagged(tagged))
		return sb.String()
	})
}
