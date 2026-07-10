package parser

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkPR75TokenizeHEEX(b *testing.B) {
	for _, expressions := range []int{50, 200, 800} {
		b.Run(fmt.Sprintf("expressions_%d", expressions), func(b *testing.B) {
			var source strings.Builder
			source.WriteString("defmodule MyApp.Page do\n  def render(assigns) do\n    ~H\"\"\"\n")
			for range expressions {
				source.WriteString("    <.card attrs={choose(%{}, SharedLib.Worker.run())} />\n")
			}
			source.WriteString("    \"\"\"\n  end\nend\n")
			text := []byte(source.String())
			b.SetBytes(int64(len(text)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				TokenizeFull(text)
			}
		})
	}
}

func BenchmarkPR75TokenizeHEEXStaticHTML(b *testing.B) {
	var source strings.Builder
	for range 800 {
		source.WriteString("<div class=\"row\"><span>text</span></div>\n")
	}
	text := []byte(source.String())
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		TokenizeHeex(text)
	}
}
