package treesitter

import (
	"unsafe"

	tree_sitter_heex "github.com/phoenixframework/tree-sitter-heex/bindings/go"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_elixir "github.com/tree-sitter/tree-sitter-elixir/bindings/go"
)

// Tree contains an Elixir document tree and a map of any HEEX sub-trees.
// Heex is a map of `quoted_content` nodes within sigils in the document
// tree to their corresponding HEEX sub-tree.
//
// (sigil (sigil_name) node: (quoted_content))
type Tree struct {
	Trunk *tree_sitter.Tree
	Heex  map[*tree_sitter.Node]*tree_sitter.Tree
}

// Close closes the trunk tree and any HEEX sub-trees.
func (t *Tree) Close() {
	for _, ht := range t.Heex {
		ht.Close()
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
	trunk := parsers[LangElixir].Parse(src, nil)
	if trunk == nil {
		return nil
	}

	heex := make(map[*tree_sitter.Node]*tree_sitter.Tree)
	visitTree(trunk.RootNode(), func(node *tree_sitter.Node) {
		if node.Kind() == "quoted_content" &&
			node.Parent().Kind() == "sigil" &&
			/* sigil_name */ node.PrevNamedSibling().Utf8Text(src) == "H" {
			tree := parsers[LangHeex].Parse(src[node.StartByte():node.EndByte()], nil)
			if tree == nil {
				return
			}

			heex[node] = tree
		}
	})

	return &Tree{
		Trunk: trunk,
		Heex:  heex,
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
			return nil
		}
		parsers[l] = p
	}

	return parsers
}

// ParseHeex parses the HEEX template in `src` and calls `onNode` for each leaf node
// it encounters. `onNode` is called with the leaf node's kind, text contents, and
// offset within the given `src` slice.
func ParseHeex(src []byte, onNode func(kind, text string, offset int)) {
	p := NewParser(LangHeex)
	if p == nil {
		return
	}
	defer p.Close()

	tree := p.Parse(src, nil)
	if tree == nil {
		return
	}
	defer tree.Close()

	visitTree(tree.RootNode(), func(node *tree_sitter.Node) {
		// notify visitor about leaf nodes
		if node.ChildCount() == 0 {
			onNode(node.Kind(), node.Utf8Text(src), int(node.StartByte()))
		}
	})
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

		for {
			// traverse via siblings, if possible
			if cursor.GotoNextSibling() {
				break
			}

			// move back up and recurse, returning once we're back to the root
			if !cursor.GotoParent() {
				return
			}
		}
	}
}
