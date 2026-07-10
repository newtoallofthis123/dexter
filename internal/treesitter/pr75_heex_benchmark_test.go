package treesitter

import (
	"fmt"
	"strings"
	"testing"
)

func buildPR75BenchmarkSource(expressions int) []byte {
	var source strings.Builder
	source.WriteString("defmodule MyApp.Page do\n  def render(assigns) do\n    ~H\"\"\"\n")
	for range expressions {
		source.WriteString("    <div>{assigns.value}</div>\n")
	}
	source.WriteString("    \"\"\"\n  end\nend\n")
	return []byte(source.String())
}

func BenchmarkPR75NewTree(b *testing.B) {
	for _, expressions := range []int{50, 200, 800} {
		b.Run(fmt.Sprintf("expressions_%d", expressions), func(b *testing.B) {
			text := buildPR75BenchmarkSource(expressions)
			b.SetBytes(int64(len(text)))
			b.ReportAllocs()
			for range b.N {
				tree := NewTree(text)
				tree.Close()
			}
		})
	}
}

func BenchmarkPR75VariableOccurrences(b *testing.B) {
	for _, expressions := range []int{50, 200, 800} {
		b.Run(fmt.Sprintf("expressions_%d", expressions), func(b *testing.B) {
			text := buildPR75BenchmarkSource(expressions)
			tree := NewTree(text)
			b.Cleanup(tree.Close)
			line := uint(3 + expressions/2)
			b.ReportAllocs()
			for range b.N {
				if got := tree.FindVariableOccurrences(text, line, 10); len(got) != expressions+1 {
					b.Fatalf("got %d occurrences, want %d", len(got), expressions+1)
				}
			}
		})
	}
}
