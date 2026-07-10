package lsp_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"go.lsp.dev/protocol"

	"github.com/remoteoss/dexter/internal/testutil/lspclient"
)

func copyHEEXFixture(t *testing.T) string {
	t.Helper()
	source := filepath.Join(repositoryRoot(t), "internal", "lsp", "testdata", "heex_app")
	target := filepath.Join(t.TempDir(), "heex_app")
	if err := os.CopyFS(target, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	return target
}

func definitionLocations(t *testing.T, client *lspclient.Client, doc *lspclient.Document, needle string, nth int) []protocol.Location {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	locations, err := client.Definition(ctx, doc, position(t, doc, needle, nth))
	if err != nil {
		t.Fatalf("definition for %q: %v\n%s", needle, err, client.Stderr())
	}
	return locations
}

func assertNoDefinition(t *testing.T, client *lspclient.Client, doc *lspclient.Document, needle string, nth int) {
	t.Helper()
	if locations := definitionLocations(t, client, doc, needle, nth); len(locations) != 0 {
		t.Fatalf("literal HEEX content %q returned definitions: %+v", needle, locations)
	}
}

func assertRenameEditCount(t *testing.T, client *lspclient.Client, doc *lspclient.Document, needle string, nth, want int) {
	t.Helper()
	edit, err := client.Rename(t.Context(), doc, position(t, doc, needle, nth), "renamed_value")
	if err != nil {
		t.Fatal(err)
	}
	if edit == nil || len(edit.Changes[doc.URI]) != want {
		t.Fatalf("rename %q edits: got %+v, want %d edits in %s", needle, edit, want, doc.URI)
	}
}

// TestPR75HEEXValidation is intentionally expected to fail on the current PR
// head. Each subtest is an independent acceptance criterion for the PR fixes.
func TestPR75HEEXValidation(t *testing.T) {
	root := copyHEEXFixture(t)
	binary := buildDexter(t)
	indexCtx, indexCancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer indexCancel()
	if err := lspclient.Index(indexCtx, binary, root); err != nil {
		t.Fatal(err)
	}

	client, err := lspclient.StartWithOptions(t.Context(), binary, root, lspclient.Options{DisableMix: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(context.Background()); err != nil {
			t.Errorf("close LSP: %v\n%s", err, client.Stderr())
		}
	})

	page, err := client.Open(t.Context(), "lib/heex_app/page_live.ex")
	if err != nil {
		t.Fatal(err)
	}

	definitionCases := []struct {
		name       string
		needle     string
		nth        int
		fileSuffix string
		lineText   string
	}{
		{"local function component", "local_card", 0, "lib/heex_app/page_live.ex", "defp local_card"},
		{"remote function component", "remote_card", 0, "lib/heex_app/components.ex", "def remote_card"},
		{"dynamic attributes", "dynamic_attrs", 0, "lib/heex_app/page_live.ex", "defp dynamic_attrs"},
		{"nested map braces", "nested_brace_call", 0, "lib/heex_app/shared_lib/worker.ex", "def nested_brace_call"},
		{"multiline interpolation", "multiline_call", 0, "lib/heex_app/shared_lib/worker.ex", "def multiline_call"},
		{"attribute value does not disable curly", "attribute_value_call", 0, "lib/heex_app/shared_lib/worker.ex", "def attribute_value_call"},
		{"EEx remains active in script", "eex_script_call", 0, "lib/heex_app/shared_lib/worker.ex", "def eex_script_call"},
		{"EEx remains active in style", "eex_style_call", 0, "lib/heex_app/shared_lib/worker.ex", "def eex_style_call"},
		{"EEx remains active under no-curly", "eex_no_curly_call", 0, "lib/heex_app/shared_lib/worker.ex", "def eex_no_curly_call"},
	}
	for _, testCase := range definitionCases {
		t.Run("definition/"+testCase.name, func(t *testing.T) {
			assertDefinitionLine(t, client, page, testCase.needle, testCase.nth, testCase.fileSuffix, testCase.lineText)
		})
	}

	t.Run("known pre-existing/UTF-16 cursor after emoji", func(t *testing.T) {
		t.Skip("not introduced by PR 75: Dexter currently treats LSP character offsets as UTF-8 bytes")
		assertDefinitionLine(t, client, page, "unicode_call", 0, "lib/heex_app/shared_lib/worker.ex", "def unicode_call")
	})

	for _, testCase := range []struct {
		name   string
		needle string
	}{
		{"script braces are literal", "script_literal_call"},
		{"style braces are literal", "style_literal_call"},
		{"no-curly descendants are literal", "no_curly_literal_call"},
	} {
		t.Run("literal/"+testCase.name, func(t *testing.T) {
			assertNoDefinition(t, client, page, testCase.needle, 0)
		})
	}

	t.Run("HEEX assign is not a module attribute", func(t *testing.T) {
		assertNoDefinition(t, client, page, "@title", 1)
	})

	t.Run("script opening-tag attributes remain active", func(t *testing.T) {
		highlights, err := client.Highlights(t.Context(), page, position(t, page, "assigns.visible?", 0))
		if err != nil {
			t.Fatal(err)
		}
		if len(highlights) != 2 {
			t.Fatalf("assigns highlights: got %d, want parameter and script attribute: %+v", len(highlights), highlights)
		}
	})

	t.Run("local component highlights include calls and declaration", func(t *testing.T) {
		highlights, err := client.Highlights(t.Context(), page, position(t, page, "local_card", 0))
		if err != nil {
			t.Fatal(err)
		}
		if len(highlights) != 3 {
			t.Fatalf("local component highlights: got %d, want 3: %+v", len(highlights), highlights)
		}
	})

	t.Run("remote component module highlights include alias and call", func(t *testing.T) {
		highlights, err := client.Highlights(t.Context(), page, position(t, page, "Components", 1))
		if err != nil {
			t.Fatal(err)
		}
		if len(highlights) != 2 {
			t.Fatalf("remote component module highlights: got %d, want alias and call: %+v", len(highlights), highlights)
		}
	})

	t.Run("local component references include calls and declaration", func(t *testing.T) {
		references, err := client.References(t.Context(), page, position(t, page, "local_card", 0), true)
		if err != nil {
			t.Fatal(err)
		}
		if len(references) != 3 {
			t.Fatalf("local component references: got %d, want 3: %+v", len(references), references)
		}
	})

	t.Run("inline local component without injector is indexed", func(t *testing.T) {
		plain, err := client.Open(t.Context(), "lib/heex_app/plain_components.ex")
		if err != nil {
			t.Fatal(err)
		}
		references, err := client.References(t.Context(), plain, position(t, plain, "isolated_card", 0), true)
		if err != nil {
			t.Fatal(err)
		}
		if len(references) != 2 {
			t.Fatalf("isolated component references: got %d, want call and declaration: %+v", len(references), references)
		}
	})

	scope, err := client.Open(t.Context(), "lib/heex_app/scope_live.ex")
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name   string
		needle string
		nth    int
	}{
		{"EEx for binding", "item", 1},
		{"first case clause", "clause_item", 1},
		{"special :for binding", "entry", 1},
		{"special :let binding", "slot_item", 1},
	} {
		t.Run("rename scope/"+testCase.name, func(t *testing.T) {
			assertRenameEditCount(t, client, scope, testCase.needle, testCase.nth, 2)
		})
	}

	t.Run("didChange preserves incomplete quoted sigil tail", func(t *testing.T) {
		editing, err := client.Open(t.Context(), "lib/heex_app/editing_live.ex")
		if err != nil {
			t.Fatal(err)
		}
		text := "defmodule HeexApp.EditingLive do\n  alias HeexApp.SharedLib.Worker\n  def render(assigns), do: ~H\"{Worker.incomplete_call"
		if err := client.Change(t.Context(), editing, text); err != nil {
			t.Fatal(err)
		}
		assertDefinitionLine(t, client, editing, "incomplete_call", 0, "lib/heex_app/shared_lib/worker.ex", "def incomplete_call")
	})

	t.Run("didChange preserves incomplete heredoc tail", func(t *testing.T) {
		editing, err := client.Open(t.Context(), "lib/heex_app/editing_live.ex")
		if err != nil {
			t.Fatal(err)
		}
		text := "defmodule HeexApp.EditingLive do\n  alias HeexApp.SharedLib.Worker\n  def render(assigns) do\n    ~H\"\"\"\n    {Worker.heredoc_tail_call"
		if err := client.Change(t.Context(), editing, text); err != nil {
			t.Fatal(err)
		}
		assertDefinitionLine(t, client, editing, "heredoc_tail_call", 0, "lib/heex_app/shared_lib/worker.ex", "def heredoc_tail_call")
	})
}

func TestPR75HEEXFixtureCompiles(t *testing.T) {
	if _, err := exec.LookPath("mix"); err != nil {
		t.Skip("mix not installed")
	}
	root := copyHEEXFixture(t)
	cmd := exec.CommandContext(t.Context(), "mix", "test")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile HEEX validation fixture: %v\n%s", err, output)
	}
}
