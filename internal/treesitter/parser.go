package treesitter

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"
)

// langForExt returns the tree-sitter language for a file extension, or nil.
func langForExt(ext string) *sitter.Language {
	switch ext {
	case ".go":
		return golang.GetLanguage()
	default:
		return nil
	}
}

// Supported returns true if the file extension has a tree-sitter grammar.
func Supported(path string) bool {
	return langForExt(strings.ToLower(filepath.Ext(path))) != nil
}

// ParseFile reads and parses a file, returning its top-level symbols.
func ParseFile(path string) ([]Symbol, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseSource(path, src)
}

// ParseSource parses source bytes and returns top-level symbols.
func ParseSource(path string, src []byte) ([]Symbol, error) {
	lang := langForExt(strings.ToLower(filepath.Ext(path)))
	if lang == nil {
		return nil, nil
	}

	parser := sitter.NewParser()
	defer parser.Close()
	parser.SetLanguage(lang)

	tree, err := parser.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	return extractGo(tree.RootNode(), src), nil
}

// extractGo walks a Go AST root and extracts top-level symbols.
func extractGo(root *sitter.Node, src []byte) []Symbol {
	var syms []Symbol
	count := int(root.ChildCount())

	for i := 0; i < count; i++ {
		child := root.Child(i)
		switch child.Type() {
		case "package_clause":
			// package_identifier is a named child, not a field.
			if nc := child.NamedChild(0); nc != nil && nc.Type() == "package_identifier" {
				syms = append(syms, Symbol{
					Name:      content(nc, src),
					Kind:      KindPackage,
					StartLine: line(child),
					EndLine:   endLine(child),
				})
			}

		case "import_declaration":
			syms = append(syms, extractImport(child, src))

		case "function_declaration":
			syms = append(syms, extractFunc(child, src))

		case "method_declaration":
			syms = append(syms, extractMethod(child, src))

		case "type_declaration":
			syms = append(syms, extractTypeDecl(child, src)...)

		case "const_declaration":
			syms = append(syms, extractConstVar(child, src, KindConst)...)

		case "var_declaration":
			syms = append(syms, extractConstVar(child, src, KindVar)...)
		}
	}
	return syms
}

func extractImport(node *sitter.Node, src []byte) Symbol {
	return Symbol{
		Name:      strings.TrimSpace(content(node, src)),
		Kind:      KindImport,
		StartLine: line(node),
		EndLine:   endLine(node),
	}
}

func extractFunc(node *sitter.Node, src []byte) Symbol {
	name := node.ChildByFieldName("name")
	params := node.ChildByFieldName("parameters")
	result := node.ChildByFieldName("result")

	sym := Symbol{
		Kind:      KindFunction,
		StartLine: line(node),
		EndLine:   endLine(node),
	}
	if name != nil {
		sym.Name = content(name, src)
	}
	sym.Signature = buildFuncSig("", sym.Name, params, result, src)
	return sym
}

func extractMethod(node *sitter.Node, src []byte) Symbol {
	name := node.ChildByFieldName("name")
	receiver := node.ChildByFieldName("receiver")
	params := node.ChildByFieldName("parameters")
	result := node.ChildByFieldName("result")

	sym := Symbol{
		Kind:      KindMethod,
		StartLine: line(node),
		EndLine:   endLine(node),
	}
	if name != nil {
		sym.Name = content(name, src)
	}

	var recvStr string
	if receiver != nil {
		recvStr = content(receiver, src)
		// Extract just the type name from receiver for Receiver field.
		sym.Receiver = extractReceiverType(receiver, src)
	}
	sym.Signature = buildFuncSig(recvStr, sym.Name, params, result, src)
	return sym
}

func extractTypeDecl(node *sitter.Node, src []byte) []Symbol {
	var syms []Symbol
	count := int(node.ChildCount())
	for i := 0; i < count; i++ {
		child := node.Child(i)
		switch child.Type() {
		case "type_spec":
			syms = append(syms, extractTypeSpec(child, src))
		case "type_alias":
			syms = append(syms, extractTypeSpec(child, src))
		}
	}
	return syms
}

func extractTypeSpec(node *sitter.Node, src []byte) Symbol {
	name := node.ChildByFieldName("name")
	typeNode := node.ChildByFieldName("type")

	sym := Symbol{
		Kind:      KindType,
		StartLine: line(node),
		EndLine:   endLine(node),
	}
	if name != nil {
		sym.Name = content(name, src)
	}
	if typeNode != nil {
		switch typeNode.Type() {
		case "struct_type":
			sym.Kind = KindStruct
			sym.Children = extractStructFields(typeNode, src)
		case "interface_type":
			sym.Kind = KindInterface
			sym.Children = extractInterfaceMethods(typeNode, src)
		}
		sym.Signature = "type " + sym.Name + " " + typeNode.Type()
	}
	return sym
}

func extractStructFields(node *sitter.Node, src []byte) []Symbol {
	var fields []Symbol
	body := node.ChildByFieldName("body")
	if body == nil {
		// struct_type has field_declaration_list as child
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			if child.Type() == "field_declaration_list" {
				body = child
				break
			}
		}
	}
	if body == nil {
		return nil
	}
	for i := 0; i < int(body.ChildCount()); i++ {
		child := body.Child(i)
		if child.Type() == "field_declaration" {
			nameNode := child.ChildByFieldName("name")
			typeNode := child.ChildByFieldName("type")
			if nameNode != nil {
				f := Symbol{
					Name:      content(nameNode, src),
					Kind:      KindVar,
					StartLine: line(child),
					EndLine:   endLine(child),
				}
				if typeNode != nil {
					f.Signature = content(nameNode, src) + " " + content(typeNode, src)
				}
				fields = append(fields, f)
			}
		}
	}
	return fields
}

func extractInterfaceMethods(node *sitter.Node, src []byte) []Symbol {
	var methods []Symbol
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "method_elem" || child.Type() == "method_spec" {
			nameNode := child.ChildByFieldName("name")
			if nameNode != nil {
				m := Symbol{
					Name:      content(nameNode, src),
					Kind:      KindMethod,
					StartLine: line(child),
					EndLine:   endLine(child),
					Signature: content(child, src),
				}
				methods = append(methods, m)
			}
		}
	}
	return methods
}

func extractConstVar(node *sitter.Node, src []byte, kind SymbolKind) []Symbol {
	var syms []Symbol
	count := int(node.ChildCount())
	for i := 0; i < count; i++ {
		child := node.Child(i)
		if child.Type() == "const_spec" || child.Type() == "var_spec" {
			nameNode := child.ChildByFieldName("name")
			if nameNode != nil {
				syms = append(syms, Symbol{
					Name:      content(nameNode, src),
					Kind:      kind,
					StartLine: line(child),
					EndLine:   endLine(child),
				})
			}
		}
	}
	return syms
}

func extractReceiverType(receiver *sitter.Node, src []byte) string {
	// Walk into parameter_list -> parameter_declaration -> type
	for i := 0; i < int(receiver.ChildCount()); i++ {
		child := receiver.Child(i)
		if child.Type() == "parameter_declaration" {
			typeNode := child.ChildByFieldName("type")
			if typeNode != nil {
				return content(typeNode, src)
			}
		}
	}
	return ""
}

func buildFuncSig(receiver, name string, params, result *sitter.Node, src []byte) string {
	var b strings.Builder
	b.WriteString("func ")
	if receiver != "" {
		b.WriteString(receiver)
		b.WriteByte(' ')
	}
	b.WriteString(name)
	if params != nil {
		b.WriteString(content(params, src))
	}
	if result != nil {
		b.WriteByte(' ')
		b.WriteString(content(result, src))
	}
	return b.String()
}

// helpers

func content(node *sitter.Node, src []byte) string {
	return node.Content(src)
}

func line(node *sitter.Node) int {
	return int(node.StartPoint().Row) + 1 // 1-indexed
}

func endLine(node *sitter.Node) int {
	return int(node.EndPoint().Row) + 1
}
