package lsp_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.lsp.dev/protocol"

	"github.com/remoteoss/dexter/internal/testutil/lspclient"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate integration test")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func copyIntegrationFixture(t *testing.T) string {
	t.Helper()
	source := filepath.Join(repositoryRoot(t), "internal", "lsp", "testdata", "integration_app")
	target := filepath.Join(t.TempDir(), "integration_app")
	if err := os.CopyFS(target, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	return target
}

func buildDexter(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "dexter")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd")
	cmd.Dir = repositoryRoot(t)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build dexter: %v\n%s", err, output)
	}
	return binary
}

func position(t *testing.T, doc *lspclient.Document, needle string, nth int) protocol.Position {
	t.Helper()
	position, err := doc.Position(needle, nth)
	if err != nil {
		t.Fatal(err)
	}
	return position
}

func assertDefinitionLine(t *testing.T, client *lspclient.Client, doc *lspclient.Document, needle string, nth int, wantFileSuffix, wantLineText string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	locations, err := client.Definition(ctx, doc, position(t, doc, needle, nth))
	if err != nil {
		t.Fatalf("definition for %q: %v\n%s", needle, err, client.Stderr())
	}
	if len(locations) == 0 {
		t.Fatalf("definition for %q returned no locations\n%s", needle, client.Stderr())
	}
	location := locations[0]
	path := strings.TrimPrefix(string(location.URI), "file://")
	if !strings.HasSuffix(filepath.ToSlash(path), filepath.ToSlash(wantFileSuffix)) {
		t.Fatalf("definition for %q returned %s, want suffix %s", needle, path, wantFileSuffix)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	line := int(location.Range.Start.Line)
	if line >= len(lines) {
		t.Fatalf("definition for %q landed beyond %s at line %d", needle, path, line)
	}
	if !strings.Contains(lines[line], wantLineText) {
		t.Fatalf("definition for %q landed on line %d (%q), want text %q", needle, line, lines[line], wantLineText)
	}
}

func TestProtocolEndToEnd(t *testing.T) {
	root := copyIntegrationFixture(t)
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

	doc, err := client.Open(t.Context(), "lib/integration_app/page.ex")
	if err != nil {
		t.Fatal(err)
	}

	assertDefinitionLine(t, client, doc, "double", 0, "lib/integration_app/math.ex", "def double")

	highlights, err := client.Highlights(t.Context(), doc, position(t, doc, "normalized", 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(highlights) != 2 {
		t.Fatalf("variable highlights: got %d, want 2: %+v", len(highlights), highlights)
	}

	references, err := client.References(t.Context(), doc, position(t, doc, "double", 0), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(references) != 2 {
		t.Fatalf("remote function references: got %d, want 2: %+v", len(references), references)
	}

	edit, err := client.Rename(t.Context(), doc, position(t, doc, "normalized", 0), "result")
	if err != nil {
		t.Fatal(err)
	}
	if edit == nil || len(edit.Changes[doc.URI]) != 2 {
		t.Fatalf("variable rename edits: %+v", edit)
	}

	changed := strings.Replace(doc.Text, "Math.double", "IntegrationApp.Math.double", 1)
	if err := client.Change(t.Context(), doc, changed); err != nil {
		t.Fatal(err)
	}
	assertDefinitionLine(t, client, doc, "double", 0, "lib/integration_app/math.ex", "def double")
}

func TestIntegrationFixtureCompiles(t *testing.T) {
	if _, err := exec.LookPath("mix"); err != nil {
		t.Skip("mix not installed")
	}
	root := copyIntegrationFixture(t)
	cmd := exec.CommandContext(t.Context(), "mix", "test")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile integration fixture: %v\n%s", err, output)
	}
}
