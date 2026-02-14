package treesitter

import (
	"fmt"
	"sort"
	"strings"
)

// MaxOutlineBytes caps the outline to avoid consuming too much of the LLM
// context window. ~16KB ≈ 4-5K tokens, enough for ~100 Go files.
const MaxOutlineBytes = 16 * 1024

// FormatOutline produces a compact YAML-like project outline for LLM system
// prompt injection. Groups methods by receiver type under each file.
// Output is capped at MaxOutlineBytes to protect the context window.
//
// Example output:
//
//	# Project Symbols
//	internal/mcp/proxy.go:
//	  Proxy: RegisterTool, CallTool, ListTools
//	  fn: NewProxy, parseRetryAfter
//	  type: ToolHandler
func FormatOutline(snap map[string][]Symbol) string {
	if len(snap) == 0 {
		return ""
	}

	paths := make([]string, 0, len(snap))
	for p := range snap {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var b strings.Builder
	b.WriteString("# Project Symbols\n")

	for _, path := range paths {
		syms := snap[path]
		text := formatFileCompact(syms)
		if text == "" {
			continue
		}
		entry := fmt.Sprintf("%s:\n%s", path, text)
		if b.Len()+len(entry) > MaxOutlineBytes {
			fmt.Fprintf(&b, "# ... truncated (%d files total)\n", len(paths))
			break
		}
		b.WriteString(entry)
	}
	return b.String()
}

// fileGroups collects symbols into categories for compact rendering.
type fileGroups struct {
	methods map[string][]string // receiver -> method names
	funcs   []string
	types   []string
	consts  []string
	vars    []string
}

func newFileGroups() *fileGroups {
	return &fileGroups{methods: make(map[string][]string)}
}

func (g *fileGroups) add(s Symbol) {
	switch s.Kind {
	case KindPackage, KindImport:
		// skip
	case KindFunction:
		g.funcs = append(g.funcs, s.Name)
	case KindMethod:
		recv := s.Receiver
		if recv == "" {
			recv = "?"
		}
		g.methods[recv] = append(g.methods[recv], s.Name)
	case KindStruct:
		g.types = append(g.types, s.Name+" (struct)")
	case KindInterface:
		g.types = append(g.types, s.Name+" (interface)")
	case KindType:
		g.types = append(g.types, s.Name)
	case KindConst:
		g.consts = append(g.consts, s.Name)
	case KindVar:
		g.vars = append(g.vars, s.Name)
	}
}

func (g *fileGroups) empty() bool {
	return len(g.funcs) == 0 && len(g.methods) == 0 &&
		len(g.types) == 0 && len(g.consts) == 0 && len(g.vars) == 0
}

func (g *fileGroups) render() string {
	var b strings.Builder

	if len(g.types) > 0 {
		fmt.Fprintf(&b, "  type: %s\n", strings.Join(g.types, ", "))
	}

	recvs := make([]string, 0, len(g.methods))
	for r := range g.methods {
		recvs = append(recvs, r)
	}
	sort.Strings(recvs)
	for _, recv := range recvs {
		fmt.Fprintf(&b, "  %s: %s\n", recv, strings.Join(g.methods[recv], ", "))
	}

	if len(g.funcs) > 0 {
		fmt.Fprintf(&b, "  fn: %s\n", strings.Join(g.funcs, ", "))
	}
	if len(g.consts) > 0 {
		fmt.Fprintf(&b, "  const: %s\n", strings.Join(g.consts, ", "))
	}
	if len(g.vars) > 0 {
		fmt.Fprintf(&b, "  var: %s\n", strings.Join(g.vars, ", "))
	}

	return b.String()
}

// formatFileCompact produces a compact per-file representation.
func formatFileCompact(syms []Symbol) string {
	g := newFileGroups()
	for _, s := range syms {
		g.add(s)
	}
	if g.empty() {
		return ""
	}
	return g.render()
}
