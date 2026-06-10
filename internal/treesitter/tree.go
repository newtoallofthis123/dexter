package treesitter

import (
	"log"
	"unsafe"

	tree_sitter_heex "github.com/phoenixframework/tree-sitter-heex/bindings/go"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_elixir "github.com/tree-sitter/tree-sitter-elixir/bindings/go"
)

// Tree contains a document trunk tree and a map of any branch sub-trees.
// For Elixir trunks, Branches is a map of `quoted_content` node IDs within sigils
// in the document tree to their corresponding HEEX sub-tree. For HEEX trunks,
// Branches is a map of `expression_value` node IDs within interpolated expressions
// in the document tree to their corresponding Elixir sub-tree. Sub-trees may
// be nested arbitrarily deep, though in practice it will typically be 1-3 levels.
//
// Elixir->HEEX: (sigil (sigil_name) node: (quoted_content))
// HEEX->Elixir: (expression node: (expression_value))
type Tree struct {
	Trunk    *tree_sitter.Tree
	Branches map[uintptr]*Tree
}

// Close closes the trunk tree and any HEEX sub-trees.
func (t *Tree) Close() {
	for _, b := range t.Branches {
		b.Close()
	}
	t.Trunk.Close()
}

// NewTree creates parsers, parses src, parses nested HEEX templates, and returns the created trees.
// Used by the standalone (non-cached) entry points. Returns nil on failure.
func NewTree(src []byte) *Tree {
	parsers := AllParsers()
	if parsers == nil {
		return nil
	}
	for _, p := range parsers {
		defer p.Close()
	}
	return NewTreeWithParsers(src, parsers)
}

// NewTreeWithParsers parses src, parses nested HEEX templates, and returns the created trees.
// Used by cached entry points . Returns nil on failure.
func NewTreeWithParsers(src []byte, parsers map[Language]*tree_sitter.Parser) *Tree {
	return newTree(LangElixir, src, parsers)
}

func newTree(lang Language, src []byte, parsers map[Language]*tree_sitter.Parser) *Tree {
	trunk := parsers[lang].Parse(src, nil)
	if trunk == nil {
		return nil
	}

	branches := make(map[uintptr]*Tree)
	visitTree(trunk.RootNode(), func(node *tree_sitter.Node) {
		// when visiting Elixir trees, parse nested ~H sigils as HEEX sub-trees
		if lang == LangElixir &&
			node.Kind() == "quoted_content" &&
			node.Parent() != nil && node.Parent().Kind() == "sigil" &&
			/* sigil_name */ node.PrevNamedSibling() != nil && node.PrevNamedSibling().Utf8Text(src) == "H" {
			log.Printf("HEEX sub-tree %d at %s", node.Id(), node.ToSexp())
			if tree := newTree(LangHeex, src[node.StartByte():node.EndByte()], parsers); tree != nil {
				branches[node.Id()] = tree
			}
		}

		// when visiting HEEX trees, parse nested expressions as Elixir sub-trees
		if lang == LangHeex && node.Kind() == "expression_value" {
			log.Printf("Elixir sub-tree %d at %s", node.Id(), node.ToSexp())
			if tree := newTree(LangElixir, src[node.StartByte():node.EndByte()], parsers); tree != nil {
				branches[node.Id()] = tree
			}
		}
	})

	return &Tree{
		Trunk:    trunk,
		Branches: branches,
	}
}

type Language byte

const (
	LangElixir Language = iota
	LangHeex
)

func NewParser(lang Language) *tree_sitter.Parser {
	var language unsafe.Pointer
	switch lang {
	case LangElixir:
		language = tree_sitter_elixir.Language()
	case LangHeex:
		language = tree_sitter_heex.Language()
	}

	p := tree_sitter.NewParser()
	if err := p.SetLanguage(tree_sitter.NewLanguage(language)); err != nil {
		return nil
	}

	return p
}

func AllParsers() map[Language]*tree_sitter.Parser {
	parsers := make(map[Language]*tree_sitter.Parser)

	for _, l := range []Language{LangElixir, LangHeex} {
		p := NewParser(l)
		if p == nil {
			// if a parser fails to initialize, close any already-opened parsers
			for _, pp := range parsers {
				pp.Close()
			}
			return nil
		}
		parsers[l] = p
	}

	return parsers
}

func visitTree(root *tree_sitter.Node, onNode func(node *tree_sitter.Node)) {
	cursor := root.Walk()
	defer cursor.Close()

	for {
		// visit current node
		onNode(cursor.Node())

		// traverse down one level, if possible
		if cursor.GotoFirstChild() {
			continue
		}

		// traverse via siblings, if possible
		for !cursor.GotoNextSibling() {
			// move back up and recurse, returning once we're back to the root
			if !cursor.GotoParent() {
				return
			}
		}
	}
}
