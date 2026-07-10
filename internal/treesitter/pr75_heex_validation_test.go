package treesitter

import (
	"strings"
	"testing"
)

func validationHasOccurrenceAt(occs []VariableOccurrence, line, start uint) bool {
	for _, occ := range occs {
		if occ.Line == line && occ.StartCol == start {
			return true
		}
	}
	return false
}

func validationPosition(src []byte, needle string) (uint, uint) {
	index := strings.Index(string(src), needle)
	prefix := string(src[:index])
	line := strings.Count(prefix, "\n")
	lineStart := strings.LastIndexByte(prefix, '\n') + 1
	return uint(line), uint(index - lineStart)
}

func TestPR75ValidationFunctionComponentTokenOccurrences(t *testing.T) {
	src := []byte("defmodule MyApp.Page do\n  use Phoenix.Component\n  def render(assigns), do: ~H\"<.card /><.card></.card>\"\n  defp card(assigns), do: ~H\"\"\nend")
	tree := NewTree(src)
	if tree == nil {
		t.Fatal("failed to parse tree")
	}
	defer tree.Close()

	occs := tree.FindTokenOccurrences(src, "card")
	for _, col := range []uint{32, 41, 49} {
		if !validationHasOccurrenceAt(occs, 2, col) {
			t.Errorf("missing component occurrence at 2:%d; got %+v", col, occs)
		}
	}
}

func TestPR75ValidationOuterVariableInsideInlineHEEX(t *testing.T) {
	src := []byte("defmodule MyApp.Page do\n  def render(assigns), do: ~H\"<div>{assigns.title}</div>\"\nend")
	tree := NewTree(src)
	if tree == nil {
		t.Fatal("failed to parse tree")
	}
	defer tree.Close()

	occs := tree.FindVariableOccurrences(src, 1, 42)
	if !validationHasOccurrenceAt(occs, 1, 13) || !validationHasOccurrenceAt(occs, 1, 36) {
		t.Fatalf("outer variable scope did not cross nested trees: %+v", occs)
	}
}

func TestPR75ValidationOuterVariableInsidePartialDirective(t *testing.T) {
	src := []byte("defmodule MyApp.Page do\n  def render(assigns) do\n    ~H\"\"\"\n    <%= if assigns.ready? do %>\n      ready\n    <% end %>\n    \"\"\"\n  end\nend")
	tree := NewTree(src)
	if tree == nil {
		t.Fatal("failed to parse tree")
	}
	defer tree.Close()

	occs := tree.FindVariableOccurrences(src, 3, 11)
	if !validationHasOccurrenceAt(occs, 1, 13) || !validationHasOccurrenceAt(occs, 3, 11) {
		t.Fatalf("outer variable scope did not enter partial EEx directive: %+v", occs)
	}
}

func TestPR75ValidationHEEXAssignIsNotModuleAttribute(t *testing.T) {
	src := []byte("defmodule MyApp.Page do\n  @title \"module attribute\"\n  def render(assigns), do: ~H\"<h1>{@title}</h1>\"\nend")
	tree := NewTree(src)
	if tree == nil {
		t.Fatal("failed to parse tree")
	}
	defer tree.Close()

	if occs := tree.FindVariableOccurrences(src, 2, 38); len(occs) != 0 {
		t.Fatalf("HEEX assign was conflated with module attribute: %+v", occs)
	}
}

func TestPR75ValidationDirectiveBindingDoesNotLeak(t *testing.T) {
	src := []byte("defmodule MyApp.Page do\n  def render(assigns) do\n    ~H\"\"\"\n    <%= for item <- assigns.items do %>\n      {item.name}\n    <% end %>\n    {item}\n    \"\"\"\n  end\nend")
	tree := NewTree(src)
	if tree == nil {
		t.Fatal("failed to parse tree")
	}
	defer tree.Close()
	occs := tree.FindVariableOccurrences(src, 4, 7)
	if validationHasOccurrenceAt(occs, 6, 5) {
		t.Fatalf("for-comprehension binding leaked beyond its EEx block: %+v", occs)
	}
	if !validationHasOccurrenceAt(occs, 3, 12) || !validationHasOccurrenceAt(occs, 4, 7) {
		t.Fatalf("missing binding/body occurrences for EEx block: %+v", occs)
	}
	if outside := tree.FindVariableOccurrences(src, 6, 5); len(outside) != 0 {
		t.Fatalf("loop-local variable resolved after EEx block: %+v", outside)
	}
}

func TestPR75ValidationDirectiveStringHashDoesNotHideDo(t *testing.T) {
	src := []byte("defmodule MyApp.Page do\n  def render(assigns) do\n    ~H\"\"\"\n    <%= for item <- [\"#\"] do %>\n      {item.name}\n    <% end %>\n    {item}\n    \"\"\"\n  end\nend")
	tree := NewTree(src)
	if tree == nil {
		t.Fatal("failed to parse tree")
	}
	defer tree.Close()

	occs := tree.FindVariableOccurrences(src, 4, 7)
	if !validationHasOccurrenceAt(occs, 3, 12) || !validationHasOccurrenceAt(occs, 4, 7) {
		t.Fatalf("string hash hid the EEx block binding: %+v", occs)
	}
	if validationHasOccurrenceAt(occs, 6, 5) {
		t.Fatalf("EEx block binding leaked after string hash: %+v", occs)
	}
}

func TestPR75ValidationDirectiveBindingShadowsOuterVariable(t *testing.T) {
	src := []byte("defmodule MyApp.Page do\n  def render(item) do\n    ~H\"\"\"\n    <%= for item <- items() do %>\n      {item.name}\n    <% end %>\n    {item}\n    \"\"\"\n  end\nend")
	tree := NewTree(src)
	if tree == nil {
		t.Fatal("failed to parse tree")
	}
	defer tree.Close()

	occs := tree.FindVariableOccurrences(src, 1, 13)
	if !validationHasOccurrenceAt(occs, 1, 13) || !validationHasOccurrenceAt(occs, 6, 5) {
		t.Fatalf("outer variable occurrences missing: %+v", occs)
	}
	if validationHasOccurrenceAt(occs, 3, 12) || validationHasOccurrenceAt(occs, 4, 7) {
		t.Fatalf("inner EEx binding conflated with outer variable: %+v", occs)
	}
}

func TestPR75ValidationSpecialForBindingDoesNotLeak(t *testing.T) {
	src := []byte("defmodule MyApp.Page do\n  def render(assigns) do\n    ~H\"\"\"\n    <li :for={item <- assigns.items}>{item.name}</li>\n    {item}\n    \"\"\"\n  end\nend")
	tree := NewTree(src)
	if tree == nil {
		t.Fatal("failed to parse tree")
	}
	defer tree.Close()

	occs := tree.FindVariableOccurrences(src, 3, 15)
	if !validationHasOccurrenceAt(occs, 3, 14) || !validationHasOccurrenceAt(occs, 3, 38) {
		t.Fatalf("special :for binding/body occurrences missing: %+v", occs)
	}
	if validationHasOccurrenceAt(occs, 4, 5) {
		t.Fatalf("special :for binding leaked beyond tag: %+v", occs)
	}
}

func TestPR75ValidationScriptAndStyleCurlyContentIsNotElixir(t *testing.T) {
	for _, src := range [][]byte{
		[]byte("defmodule MyApp.Page do\n  def render(value), do: ~H\"<script>const value = {value};</script>\"\nend"),
		[]byte("defmodule MyApp.Page do\n  def render(value), do: ~H\"<style>.row {value}</style>\"\nend"),
	} {
		tree := NewTree(src)
		if tree == nil {
			t.Fatal("failed to parse tree")
		}
		line, col := validationPosition(src, "{value}")
		if occs := tree.FindVariableOccurrences(src, line, col+1); len(occs) != 0 {
			t.Errorf("raw-tag curly content was parsed as Elixir: %+v", occs)
		}
		tree.Close()
	}
}

func TestPR75ValidationNoCurlyAttributeContentIsNotElixir(t *testing.T) {
	src := []byte("defmodule MyApp.Page do\n  def render(value), do: ~H\"<div phx-no-curly-interpolation>{value}</div>\"\nend")
	tree := NewTree(src)
	if tree == nil {
		t.Fatal("failed to parse tree")
	}
	defer tree.Close()

	if occs := tree.FindVariableOccurrences(src, 1, 61); len(occs) != 0 {
		t.Fatalf("phx-no-curly-interpolation content was parsed as Elixir: %+v", occs)
	}
}

func TestPR75ValidationNoCurlyTextInAttributeValueDoesNotDisable(t *testing.T) {
	src := []byte("defmodule MyApp.Page do\n  def render(value), do: ~H[<div data-label=\"phx-no-curly-interpolation\">{value}</div>]\nend")
	tree := NewTree(src)
	if tree == nil {
		t.Fatal("failed to parse tree")
	}
	defer tree.Close()

	occs := tree.FindVariableOccurrences(src, 1, 74)
	if !validationHasOccurrenceAt(occs, 1, 13) || !validationHasOccurrenceAt(occs, 1, 74) {
		t.Fatalf("attribute value text disabled real interpolation: %+v", occs)
	}
}

func TestPR75ValidationScriptAttributeStillInterpolates(t *testing.T) {
	src := []byte("defmodule MyApp.Page do\n  def render(value), do: ~H\"<script data-value={value}>const literal = {value}</script>\"\nend")
	tree := NewTree(src)
	if tree == nil {
		t.Fatal("failed to parse tree")
	}
	defer tree.Close()

	occs := tree.FindVariableOccurrences(src, 1, 48)
	if len(occs) != 2 || !validationHasOccurrenceAt(occs, 1, 13) || !validationHasOccurrenceAt(occs, 1, 48) {
		t.Fatalf("script attribute interpolation mismatch: %+v", occs)
	}
}

func TestPR75ValidationEExClauseBindingsAreIsolated(t *testing.T) {
	src := []byte("defmodule MyApp.Page do\n  def render(assigns) do\n    ~H\"\"\"\n    <%= case assigns.result do %>\n      <% {:ok, item} -> %>\n        {item.name}\n      <% {:error, item} -> %>\n        {item.message}\n    <% end %>\n    {item}\n    \"\"\"\n  end\nend")
	tree := NewTree(src)
	if tree == nil {
		t.Fatal("failed to parse tree")
	}
	defer tree.Close()
	occs := tree.FindVariableOccurrences(src, 5, 9)
	if !validationHasOccurrenceAt(occs, 4, 15) || !validationHasOccurrenceAt(occs, 5, 9) {
		t.Fatalf("first case-clause binding/body occurrences missing: %+v", occs)
	}
	for _, position := range [][2]uint{{6, 18}, {7, 9}, {9, 5}} {
		if validationHasOccurrenceAt(occs, position[0], position[1]) {
			t.Fatalf("case-clause binding leaked to %d:%d: %+v", position[0], position[1], occs)
		}
	}
}
