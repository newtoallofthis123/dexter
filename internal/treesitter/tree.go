package treesitter

import (
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
// For nested sub-trees, Root points back to the parent tree that contains the
// sub-tree. Navigation is possible both up (using Parent()) and down (using Child(i)).
//
// Elixir->HEEX: (sigil (sigil_name) node: (quoted_content))
// HEEX->Elixir: (expression node: (expression_value))
type Tree struct {
	Root     *TreeNode
	Trunk    *tree_sitter.Tree
	Branches map[uintptr]*Tree
	Language Language
}

// TrunkNode returns a TreeNode pointing to the root node of the trunk.
func (t *Tree) TrunkNode() *TreeNode {
	return &TreeNode{Tree: t, Node: t.Trunk.RootNode()}
}

// Close recursively closes the trunk tree and any branch sub-trees.
func (t *Tree) Close() {
	for _, b := range t.Branches {
		b.Close()
	}
	t.Trunk.Close()
}

// TreeNode represents a node within a tree or sub-tree.
// This facilitates traversal between trunk trees and branch sub-trees.
type TreeNode struct {
	Tree *Tree
	Node *tree_sitter.Node
}

// See tree_sitter.Node.Kind().
func (tn *TreeNode) Kind() string {
	return tn.Node.Kind()
}

// See tree_sitter.Node.IsNamed().
func (tn *TreeNode) IsNamed() bool {
	return tn.Node.IsNamed()
}

// See tree_sitter.Node.StartByte().
func (tn *TreeNode) StartByte() uint {
	if tn.Tree.Root == nil {
		return tn.Node.StartByte()
	}
	return tn.Tree.Root.StartByte() + tn.Node.StartByte()
}

// See tree_sitter.Node.EndByte().
func (tn *TreeNode) EndByte() uint {
	if tn.Tree.Root == nil {
		return tn.Node.EndByte()
	}
	return tn.Tree.Root.StartByte() + tn.Node.EndByte()
}

// Parent returns the node containing the given node in the tree, or the node
// in the root tree that contains the node if the node is the root of a branch
// sub-tree. If the node is the top-most root, returns nil.
func (tn *TreeNode) Parent() *TreeNode {
	if parent := tn.Node.Parent(); parent != nil {
		return &TreeNode{Tree: tn.Tree, Node: parent}
	}
	return tn.Tree.Root
}

// ChildCount returns the number of children for the given node, returning
// 1 for nodes that link to a branch sub-tree.
func (tn *TreeNode) ChildCount() uint {
	if branch := tn.Tree.Branches[tn.Node.Id()]; branch != nil {
		return 1
	}
	return tn.Node.ChildCount()
}

// Child returns the tree/child of the given node, moving into a sub-tree if
// the node links to a branch sub-tree.
func (tn *TreeNode) Child(i uint) *TreeNode {
	if branch := tn.Tree.Branches[tn.Node.Id()]; branch != nil {
		return branch.TrunkNode()
	}
	return &TreeNode{Tree: tn.Tree, Node: tn.Node.Child(i)}
}

// StartPosition returns the (row, col) start position of the given node
// within the top-most root tree.
func (tn *TreeNode) StartPosition() tree_sitter.Point {
	if tn.Tree.Root == nil {
		return tn.Node.StartPosition()
	}
	p := tn.Tree.Root.StartPosition()
	sp := tn.Node.StartPosition()
	p.Row += sp.Row
	if sp.Row == 0 {
		p.Column += sp.Column
	} else {
		p.Column = sp.Column
	}
	return p
}

// EndPosition returns the (row, col) end position of the given node
// within the top-most root tree.
func (tn *TreeNode) EndPosition() tree_sitter.Point {
	if tn.Tree.Root == nil {
		return tn.Node.EndPosition()
	}
	p := tn.Tree.Root.StartPosition()
	ep := tn.Node.EndPosition()
	p.Row += ep.Row
	if ep.Row == 0 {
		p.Column += ep.Column
	} else {
		p.Column = ep.Column
	}
	return p
}

// Utf8Text returns the UTF-8 encoded string representation of the given node
// within the top-most root tree.
func (tn *TreeNode) Utf8Text(src []byte) string {
	if tn.Tree.Root == nil {
		return tn.Node.Utf8Text(src)
	}
	return tn.Tree.Root.Utf8Text(src)[tn.Node.StartByte():tn.Node.EndByte()]
}

// ContainsPosition returns true if the node contains the given position
// in the top-most root tree. Tree-sitter end positions are exclusive,
// consistent with nodeAtPosition.
func (tn *TreeNode) ContainsPosition(line, col uint) bool {
	start := tn.StartPosition()
	end := tn.EndPosition()
	if line < uint(start.Row) || line > uint(end.Row) {
		return false
	}
	if line == uint(start.Row) && col < uint(start.Column) {
		return false
	}
	if line == uint(end.Row) && col >= uint(end.Column) {
		return false
	}
	return true
}

// ChildAtPosition find the deepest (most specific) child node at the given position
// within the top-most root tree.
func (tn *TreeNode) ChildAtPosition(line, col uint) *TreeNode {
	// Check if position is within this node
	if tn == nil || !tn.ContainsPosition(line, col) {
		return nil
	}

	// Try to find a more specific child
	for i := uint(0); i < tn.ChildCount(); i++ {
		if found := tn.Child(i).ChildAtPosition(line, col); found != nil {
			return found
		}
	}

	return tn
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

	t := &Tree{
		Language: lang,
		Trunk:    trunk,
		Branches: make(map[uintptr]*Tree),
	}

	visitTree(trunk.RootNode(), func(node *tree_sitter.Node) {
		// when visiting Elixir trees, parse nested ~H sigils as HEEX sub-trees
		if lang == LangElixir &&
			node.Kind() == "quoted_content" &&
			node.Parent() != nil && node.Parent().Kind() == "sigil" &&
			/* sigil_name */ node.PrevNamedSibling() != nil && node.PrevNamedSibling().Utf8Text(src) == "H" {
			if tree := newTree(LangHeex, src[node.StartByte():node.EndByte()], parsers); tree != nil {
				tree.Root = &TreeNode{Tree: t, Node: node}
				t.Branches[node.Id()] = tree
			}
		}

		// when visiting HEEX trees, parse nested expressions as Elixir sub-trees
		if lang == LangHeex && node.Kind() == "expression_value" {
			if tree := newTree(LangElixir, src[node.StartByte():node.EndByte()], parsers); tree != nil {
				tree.Root = &TreeNode{Tree: t, Node: node}
				t.Branches[node.Id()] = tree
			}
		}
	})

	return t
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
