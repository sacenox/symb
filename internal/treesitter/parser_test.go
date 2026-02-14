package treesitter

import (
	"strings"
	"testing"
)

func TestParseSource_Go(t *testing.T) {
	src := []byte(`package main

import "fmt"

const Version = "1.0"

var Debug bool

type Server struct {
	addr string
	port int
}

type Handler interface {
	Handle(req string) string
}

func main() {
	fmt.Println("hello")
}

func (s *Server) Start() error {
	return nil
}
`)

	syms, err := ParseSource("test.go", src)
	if err != nil {
		t.Fatalf("ParseSource: %v", err)
	}

	// Check we got the expected symbols (name+kind pairs to handle duplicates like "main")
	type symKey struct {
		name string
		kind SymbolKind
	}
	want := []symKey{
		{"main", KindPackage},
		{"Version", KindConst},
		{"Debug", KindVar},
		{"Server", KindStruct},
		{"Handler", KindInterface},
	}

	got := make(map[symKey]bool)
	for _, s := range syms {
		got[symKey{s.Name, s.Kind}] = true
	}

	for _, w := range want {
		if !got[w] {
			t.Errorf("missing symbol %q (kind=%v)", w.name, w.kind)
		}
	}

	// Check functions/methods
	var hasMainFunc, hasStartMethod bool
	for _, s := range syms {
		if s.Kind == KindFunction && s.Name == "main" {
			hasMainFunc = true
		}
		if s.Kind == KindMethod && s.Name == "Start" && s.Receiver == "*Server" {
			hasStartMethod = true
		}
	}
	if !hasMainFunc {
		t.Error("missing func main")
	}
	if !hasStartMethod {
		t.Error("missing method Start on *Server")
	}
}

func TestParseSource_Unsupported(t *testing.T) {
	syms, err := ParseSource("test.py", []byte("print('hello')"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(syms) != 0 {
		t.Errorf("expected no symbols for unsupported language, got %d", len(syms))
	}
}

func TestFormatOutline(t *testing.T) {
	snap := map[string][]Symbol{
		"main.go": {
			{Name: "main", Kind: KindPackage},
			{Name: "main", Kind: KindFunction},
			{Name: "Server", Kind: KindStruct},
			{Name: "Start", Kind: KindMethod, Receiver: "*Server"},
		},
	}
	out := FormatOutline(snap)
	if out == "" {
		t.Fatal("empty outline")
	}
	// New compact format checks
	if !strings.Contains(out, "fn: main") {
		t.Errorf("missing fn: main in outline:\n%s", out)
	}
	if !strings.Contains(out, "Server (struct)") {
		t.Errorf("missing Server (struct) in outline:\n%s", out)
	}
	if !strings.Contains(out, "*Server: Start") {
		t.Errorf("missing *Server: Start in outline:\n%s", out)
	}
}
