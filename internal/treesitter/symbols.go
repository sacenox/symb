// Package treesitter provides tree-sitter based code parsing for structural
// symbol extraction. Used to build project-wide context for LLM awareness.
package treesitter

// SymbolKind classifies extracted symbols.
type SymbolKind int

const (
	KindPackage SymbolKind = iota
	KindImport
	KindFunction
	KindMethod
	KindType
	KindStruct
	KindInterface
	KindConst
	KindVar
)

// Symbol represents a single extracted code symbol.
type Symbol struct {
	Name      string
	Kind      SymbolKind
	Signature string // e.g. "func (p *Proxy) CallTool(ctx context.Context, ...)"
	StartLine int    // 1-indexed
	EndLine   int    // 1-indexed
	Receiver  string // method receiver type, empty for functions
	Children  []Symbol
}

// KindString returns a short label for the symbol kind.
func (k SymbolKind) String() string {
	switch k {
	case KindPackage:
		return "pkg"
	case KindImport:
		return "import"
	case KindFunction:
		return "func"
	case KindMethod:
		return "method"
	case KindType:
		return "type"
	case KindStruct:
		return "struct"
	case KindInterface:
		return "interface"
	case KindConst:
		return "const"
	case KindVar:
		return "var"
	default:
		return "unknown"
	}
}
