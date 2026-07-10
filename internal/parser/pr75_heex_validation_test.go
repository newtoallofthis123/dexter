package parser

import (
	"slices"
	"testing"
)

func validationTokenTexts(src string) []string {
	result := TokenizeHeex([]byte(src))
	texts := make([]string, 0, len(result.Tokens))
	for _, token := range result.Tokens {
		if token.Kind == TokIdent || token.Kind == TokModule {
			texts = append(texts, TokenText([]byte(src), token))
		}
	}
	return texts
}

func TestPR75ValidationNestedBraceInterpolationKeepsFollowingCalls(t *testing.T) {
	src := `<div>{choose(%{}, SharedLib.Worker.run())}</div>`
	texts := validationTokenTexts(src)
	for _, want := range []string{"choose", "SharedLib", "Worker", "run"} {
		if !slices.Contains(texts, want) {
			t.Errorf("missing %q after nested brace; tokens: %v", want, texts)
		}
	}
}

func TestPR75ValidationDynamicAttributeExpressionIsTokenized(t *testing.T) {
	for _, src := range []string{
		`<div {dynamic_attrs()}>content</div>`,
		`<div class="row" {dynamic_attrs()} />`,
	} {
		texts := validationTokenTexts(src)
		if !slices.Contains(texts, "dynamic_attrs") {
			t.Errorf("dynamic attribute expression was skipped in %q; tokens: %v", src, texts)
		}
	}
}

func TestPR75ValidationInterpolationLineStarts(t *testing.T) {
	src := "<div>{\n  SharedLib.Worker.run()\n}</div>\n<p />"
	result := TokenizeHeex([]byte(src))
	want := []int{0, 7, 32, 40}
	if !slices.Equal(result.LineStarts, want) {
		t.Fatalf("line starts mismatch: got %v, want %v", result.LineStarts, want)
	}
}

func TestPR75ValidationCurlyInterpolationDisabledInRawTags(t *testing.T) {
	for _, src := range []string{
		`<script>if (a < b) {Ignored.Script.call()}; <%= Kept.Module.call() %></script>`,
		`<style>.row {Ignored.Style.call()} <%= Kept.Module.call() %></style>`,
	} {
		texts := validationTokenTexts(src)
		want := []string{"Kept", "Module", "call"}
		if !slices.Equal(texts, want) {
			t.Errorf("raw tag interpolation mismatch for %q: got %v, want %v", src, texts, want)
		}
	}
}

func TestPR75ValidationNoCurlyInterpolationAttribute(t *testing.T) {
	src := `<div phx-no-curly-interpolation><span>{Ignored.Module.call()}</span><%= Kept.Module.call() %></div>`
	texts := validationTokenTexts(src)
	want := []string{"Kept", "Module", "call"}
	if !slices.Equal(texts, want) {
		t.Fatalf("phx-no-curly-interpolation mismatch: got %v, want %v", texts, want)
	}
}

func TestPR75ValidationNoCurlyInterpolationEndsAtClosingTag(t *testing.T) {
	src := `<section phx-no-curly-interpolation><div>{Ignored.Module.call()}</div></section><p>{Kept.Module.call()}</p>`
	texts := validationTokenTexts(src)
	want := []string{"Kept", "Module", "call"}
	if !slices.Equal(texts, want) {
		t.Fatalf("disabled-curly state escaped its tag: got %v, want %v", texts, want)
	}
}

func TestPR75ValidationNoCurlyTextInAttributeValueDoesNotDisable(t *testing.T) {
	src := `<div data-label="phx-no-curly-interpolation">{Kept.Module.call()}</div>`
	texts := validationTokenTexts(src)
	want := []string{"Kept", "Module", "call"}
	if !slices.Equal(texts, want) {
		t.Fatalf("attribute value text disabled interpolation: got %v, want %v", texts, want)
	}
}

func TestPR75ValidationLocalComponentIndexedWithoutImports(t *testing.T) {
	src := `defmodule MyApp.Page do
  defmacro sigil_H({:<<>>, _, [contents]}, _), do: contents
  def render(assigns), do: ~H"<.card />"

  def render_block(assigns) do
    ~H"<.card />"
  end

  defp card(assigns), do: ~H""
end`
	_, refs, err := ParseText("page.ex", src)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, ref := range refs {
		if ref.Module == "MyApp.Page" && ref.Function == "card" {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("got %d local component calls without an unrelated import/use, want 2: %+v", found, refs)
	}
}

func TestPR75ValidationIncompleteHEEXSigilsKeepAllContent(t *testing.T) {
	for _, src := range []string{
		"~H\"\"\"\n{foo}",
		`~H"<.card`,
		`~H[<.card`,
	} {
		result := TokenizeFull([]byte(src))
		texts := make([]string, 0, len(result.Tokens))
		for _, token := range result.Tokens {
			if token.Kind == TokIdent {
				texts = append(texts, TokenText([]byte(src), token))
			}
		}
		if len(texts) != 1 || (texts[0] != "foo" && texts[0] != "card") {
			t.Errorf("incomplete sigil lost trailing content for %q: %v", src, texts)
		}
	}
}
