package modal

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

var testColors = Colors{Dim: "#666", SelFg: "#fff", SelBg: "#444", Border: "#555"}

func fruits(query string) []Item {
	all := []Item{
		{Name: "apple"},
		{Name: "banana"},
		{Name: "cherry"},
	}
	if query == "" {
		return all
	}
	var out []Item
	for _, it := range all {
		for _, r := range query {
			for _, c := range it.Name {
				if r == c {
					out = append(out, it)
					goto next
				}
			}
		}
	next:
	}
	return out
}

func key(ch rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: ch, Text: string(ch)}
}

func special(name string) tea.KeyPressMsg {
	switch name {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	default:
		return tea.KeyPressMsg{}
	}
}

func TestEscapeCloses(t *testing.T) {
	m := New(fruits, "> ", testColors)
	a, _ := m.HandleMsg(special("esc"))
	if _, ok := a.(ActionClose); !ok {
		t.Fatalf("expected ActionClose, got %T", a)
	}
}

func TestEnterSelectsFirst(t *testing.T) {
	m := New(fruits, "> ", testColors)
	a, _ := m.HandleMsg(special("enter"))
	sel, ok := a.(ActionSelect)
	if !ok {
		t.Fatalf("expected ActionSelect, got %T", a)
	}
	if sel.Item.Name != "apple" {
		t.Fatalf("expected apple, got %s", sel.Item.Name)
	}
}

func TestDownThenEnterSelectsHighlighted(t *testing.T) {
	m := New(fruits, "> ", testColors)
	m.HandleMsg(special("down")) // enter list, selected=0
	m.HandleMsg(special("down")) // selected=1
	a, _ := m.HandleMsg(special("enter"))
	sel, ok := a.(ActionSelect)
	if !ok {
		t.Fatalf("expected ActionSelect, got %T", a)
	}
	if sel.Item.Name != "banana" {
		t.Fatalf("expected banana, got %s", sel.Item.Name)
	}
}

func TestUpFromTopReturnsFocusToInput(t *testing.T) {
	m := New(fruits, "> ", testColors)
	m.HandleMsg(special("down")) // enter list
	if !m.inList {
		t.Fatal("expected inList=true")
	}
	m.HandleMsg(special("up")) // back to input
	if m.inList {
		t.Fatal("expected inList=false")
	}
}

func TestTypingProducesDebounceCmd(t *testing.T) {
	m := New(fruits, "> ", testColors)
	_, cmd := m.HandleMsg(key('a'))
	if cmd == nil {
		t.Fatal("expected debounce cmd")
	}
	if string(m.input) != "a" {
		t.Fatalf("expected input 'a', got %q", string(m.input))
	}
}

func TestDebounceFiresSearch(t *testing.T) {
	called := false
	searchFn := func(q string) []Item {
		if q == "x" {
			called = true
		}
		return nil
	}
	m := New(searchFn, "> ", testColors)
	// Type 'x'.
	m.HandleMsg(key('x'))
	seq := m.seq
	// Fire matching debounce.
	m.HandleMsg(debounceMsg{seq: seq})
	if !called {
		t.Fatal("expected search to be called")
	}
}

func TestStaleDebounceIgnored(t *testing.T) {
	callCount := 0
	searchFn := func(q string) []Item {
		if q != "" {
			callCount++
		}
		return nil
	}
	m := New(searchFn, "> ", testColors)
	m.HandleMsg(key('a'))
	staleSeq := m.seq
	m.HandleMsg(key('b')) // bumps seq
	// Fire stale debounce — should be ignored.
	m.HandleMsg(debounceMsg{seq: staleSeq})
	if callCount != 0 {
		t.Fatalf("expected 0 search calls for stale debounce, got %d", callCount)
	}
}

func TestBackspaceRemovesChar(t *testing.T) {
	m := New(fruits, "> ", testColors)
	m.HandleMsg(key('a'))
	m.HandleMsg(key('b'))
	m.HandleMsg(special("backspace"))
	if string(m.input) != "a" {
		t.Fatalf("expected 'a', got %q", string(m.input))
	}
}

func TestViewRenders(t *testing.T) {
	m := New(fruits, "> ", testColors)
	v := m.View(100, 40)
	if v == "" {
		t.Fatal("expected non-empty view")
	}
}

func TestEmptyResultsEnterNoAction(t *testing.T) {
	m := New(func(string) []Item { return nil }, "> ", testColors)
	a, _ := m.HandleMsg(special("enter"))
	if a != nil {
		t.Fatalf("expected nil action, got %T", a)
	}
}
