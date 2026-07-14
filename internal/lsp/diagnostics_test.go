package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/remoteoss/dexter/internal/store"
)

// fakeDiagClient captures PublishDiagnostics calls; all other Client methods
// are no-ops. Only the fields we assert on are recorded.
type fakeDiagClient struct {
	mu        sync.Mutex
	published map[protocol.DocumentURI][]protocol.Diagnostic
}

func newFakeDiagClient() *fakeDiagClient {
	return &fakeDiagClient{published: make(map[protocol.DocumentURI][]protocol.Diagnostic)}
}

func (c *fakeDiagClient) PublishDiagnostics(ctx context.Context, params *protocol.PublishDiagnosticsParams) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.published[params.URI] = params.Diagnostics
	return nil
}

func (c *fakeDiagClient) get(u protocol.DocumentURI) ([]protocol.Diagnostic, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.published[u]
	return d, ok
}

func (c *fakeDiagClient) Progress(context.Context, *protocol.ProgressParams) error { return nil }
func (c *fakeDiagClient) WorkDoneProgressCreate(context.Context, *protocol.WorkDoneProgressCreateParams) error {
	return nil
}
func (c *fakeDiagClient) LogMessage(context.Context, *protocol.LogMessageParams) error { return nil }
func (c *fakeDiagClient) ShowMessage(context.Context, *protocol.ShowMessageParams) error {
	return nil
}
func (c *fakeDiagClient) ShowMessageRequest(context.Context, *protocol.ShowMessageRequestParams) (*protocol.MessageActionItem, error) {
	return nil, nil
}
func (c *fakeDiagClient) Telemetry(context.Context, interface{}) error { return nil }
func (c *fakeDiagClient) RegisterCapability(context.Context, *protocol.RegistrationParams) error {
	return nil
}
func (c *fakeDiagClient) UnregisterCapability(context.Context, *protocol.UnregistrationParams) error {
	return nil
}
func (c *fakeDiagClient) ApplyEdit(context.Context, *protocol.ApplyWorkspaceEditParams) (bool, error) {
	return false, nil
}
func (c *fakeDiagClient) Configuration(context.Context, *protocol.ConfigurationParams) ([]interface{}, error) {
	return nil, nil
}
func (c *fakeDiagClient) WorkspaceFolders(context.Context) ([]protocol.WorkspaceFolder, error) {
	return nil, nil
}

// === Mapping ===

func TestMapDiagnosticSeverity(t *testing.T) {
	cases := map[byte]protocol.DiagnosticSeverity{
		0:   protocol.DiagnosticSeverityError,
		1:   protocol.DiagnosticSeverityWarning,
		2:   protocol.DiagnosticSeverityInformation,
		3:   protocol.DiagnosticSeverityHint,
		255: protocol.DiagnosticSeverityWarning, // unknown → warning
	}
	for sev, want := range cases {
		if got := mapDiagnosticSeverity(sev); got != want {
			t.Errorf("severity %d: got %v want %v", sev, got, want)
		}
	}
}

func TestMapCompileDiagnosticPositions(t *testing.T) {
	tests := []struct {
		name                           string
		in                             compileDiag
		wantSL, wantSC, wantEL, wantEC uint32
	}{
		{
			name:   "unknown position",
			in:     compileDiag{}, // all zero
			wantSL: 0, wantSC: 0, wantEL: 0, wantEC: 0,
		},
		{
			name:   "line only (mix integer position)",
			in:     compileDiag{startLine: 10, endLine: 10}, // normalized upstream to {10,0,10,0}
			wantSL: 9, wantSC: 0, wantEL: 9, wantEC: 0,
		},
		{
			name:   "line and column (mix {line, col})",
			in:     compileDiag{startLine: 3, startCol: 5, endLine: 3, endCol: 5},
			wantSL: 2, wantSC: 4, wantEL: 2, wantEC: 4,
		},
		{
			name:   "full range with span",
			in:     compileDiag{startLine: 3, startCol: 5, endLine: 3, endCol: 11},
			wantSL: 2, wantSC: 4, wantEL: 2, wantEC: 10,
		},
		{
			name:   "multi-line range",
			in:     compileDiag{startLine: 1, startCol: 13, endLine: 4, endCol: 6},
			wantSL: 0, wantSC: 12, wantEL: 3, wantEC: 5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapCompileDiagnostic(tt.in)
			if got.Range.Start.Line != tt.wantSL || got.Range.Start.Character != tt.wantSC ||
				got.Range.End.Line != tt.wantEL || got.Range.End.Character != tt.wantEC {
				t.Errorf("range = (%d,%d)-(%d,%d), want (%d,%d)-(%d,%d)",
					got.Range.Start.Line, got.Range.Start.Character, got.Range.End.Line, got.Range.End.Character,
					tt.wantSL, tt.wantSC, tt.wantEL, tt.wantEC)
			}
		})
	}
}

// === Diagnostic store merge ===

func TestDiagnosticStoreMergesSources(t *testing.T) {
	client := newFakeDiagClient()
	store := newDiagnosticStore(func() protocol.Client { return client })
	u := protocol.DocumentURI("file:///a.ex")

	syntaxDiag := protocol.Diagnostic{Message: "syntax error", Severity: protocol.DiagnosticSeverityError}
	compileDiag := protocol.Diagnostic{Message: "unused var", Severity: protocol.DiagnosticSeverityWarning}

	store.set(u, diagSourceSyntax, []protocol.Diagnostic{syntaxDiag})
	if got, _ := client.get(u); len(got) != 1 || got[0].Message != "syntax error" {
		t.Fatalf("after syntax set: %+v", got)
	}

	store.set(u, diagSourceCompile, []protocol.Diagnostic{compileDiag})
	got, _ := client.get(u)
	if len(got) != 2 {
		t.Fatalf("after compile set: expected 2 diagnostics, got %d", len(got))
	}

	// Clearing syntax leaves compile intact.
	store.set(u, diagSourceSyntax, nil)
	got, _ = client.get(u)
	if len(got) != 1 || got[0].Message != "unused var" {
		t.Fatalf("after clearing syntax: %+v", got)
	}

	// Clearing compile empties the document.
	store.set(u, diagSourceCompile, nil)
	got, _ = client.get(u)
	if len(got) != 0 {
		t.Fatalf("after clearing compile: expected empty, got %+v", got)
	}
}

// === Publish-diff lifecycle ===

func TestDiagManagerPublishDiffClearsFixedFiles(t *testing.T) {
	fileA := "/proj/lib/a.ex"
	fileB := "/proj/lib/b.ex"
	uriA := protocol.DocumentURI(uri.File(fileA))
	uriB := protocol.DocumentURI(uri.File(fileB))

	var mu sync.Mutex
	published := make(map[protocol.DocumentURI][]protocol.Diagnostic)
	setFn := func(u protocol.DocumentURI, diags []protocol.Diagnostic) {
		mu.Lock()
		published[u] = diags
		mu.Unlock()
	}
	get := func(u protocol.DocumentURI) []protocol.Diagnostic {
		mu.Lock()
		defer mu.Unlock()
		return published[u]
	}

	round := make(chan []compileDiag)
	compile := func(ctx context.Context, root string) ([]compileDiag, error) {
		return <-round, nil
	}

	m := newDiagManager(compile, setFn, nil, nil, func(string, ...interface{}) {})
	m.idle = 0

	// Round 1: both files have a warning.
	m.trigger("/proj")
	round <- []compileDiag{
		{severity: 1, startLine: 1, file: fileA, message: "a warn"},
		{severity: 1, startLine: 1, file: fileB, message: "b warn"},
	}
	waitFor(t, func() bool { return len(get(uriA)) == 1 && len(get(uriB)) == 1 })

	// Round 2: file A is fixed and drops out; B still warns. A must be cleared
	// (published as an empty array) while B stays.
	m.trigger("/proj")
	round <- []compileDiag{
		{severity: 1, startLine: 1, file: fileB, message: "b warn"},
	}
	waitFor(t, func() bool { return len(get(uriA)) == 0 && len(get(uriB)) == 1 })
}

// === Single-flight + coalescing ===

func TestDiagManagerSingleFlightCoalesces(t *testing.T) {
	var calls int32
	started := make(chan struct{}, 10)
	gate := make(chan struct{})

	compile := func(ctx context.Context, root string) ([]compileDiag, error) {
		atomic.AddInt32(&calls, 1)
		started <- struct{}{}
		<-gate
		return nil, nil
	}

	m := newDiagManager(compile, func(protocol.DocumentURI, []protocol.Diagnostic) {}, nil, nil, func(string, ...interface{}) {})
	m.idle = 0

	m.trigger("/proj")
	<-started // first compile running

	// Two more triggers while the first is in flight coalesce into one.
	m.trigger("/proj")
	m.trigger("/proj")

	gate <- struct{}{} // release first compile
	<-started          // coalesced second compile runs
	gate <- struct{}{} // release second compile

	// No third compile should start.
	select {
	case <-started:
		t.Fatal("a third compile ran; coalescing failed")
	case <-time.After(100 * time.Millisecond):
	}

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected exactly 2 compiles, got %d", got)
	}
}

// === Syntax checker version guard ===

func TestSyntaxCheckerVersionGuardDropsStale(t *testing.T) {
	var mu sync.Mutex
	published := make(map[protocol.DocumentURI][]protocol.Diagnostic)
	publishFn := func(u protocol.DocumentURI, diags []protocol.Diagnostic) {
		mu.Lock()
		published[u] = diags
		mu.Unlock()
	}

	// check blocks until released so we can bump the version mid-flight.
	release := make(chan struct{})
	check := func(ctx context.Context, buildRoot, content string) (*syntaxResult, error) {
		<-release
		return &syntaxResult{ok: false, line: 1, col: 1, message: "boom"}, nil
	}

	c := newSyntaxChecker(time.Millisecond, check, publishFn, func(string, ...interface{}) {})
	docURI := "file:///a.ex"

	c.schedule(docURI, 1, "bad", "/build")
	// Bump to a newer version before the in-flight check completes.
	waitFor(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.versions[docURI] == 1
	})
	c.mu.Lock()
	c.versions[docURI] = 2
	c.mu.Unlock()

	close(release) // let the stale v1 check finish

	// The stale result must be dropped: nothing published.
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	_, ok := published[protocol.DocumentURI(docURI)]
	mu.Unlock()
	if ok {
		t.Fatal("stale (v1) syntax result was published despite version bump to v2")
	}
}

// === Integration: real BEAM ===

func writeDiagFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "lib"), 0755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, content string) {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("mix.exs", `defmodule DiagFixture.MixProject do
  use Mix.Project

  def project do
    [app: :diag_fixture, version: "0.1.0", elixir: "~> 1.15"]
  end
end
`)
	write("lib/warn.ex", `defmodule DiagFixture.Warn do
  def run do
    unused = 42
    :ok
  end
end
`)
	write("lib/err.ex", `defmodule DiagFixture.Err do
  def go, do: DiagFixture.Nope.missing(1)
end
`)
	return root
}

func TestCompileDiagnostics_Fixture(t *testing.T) {
	mixPath, err := exec.LookPath("mix")
	if err != nil {
		t.Skip("mix not available in PATH")
	}

	root := writeDiagFixture(t)

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(st, root)
	server.mixBin = mixPath
	t.Cleanup(func() {
		server.closeDiagBeams()
		_ = st.Close()
	})

	ctx := context.Background()

	// First compile: both files report a warning.
	diags, err := server.runProjectCompile(ctx, root)
	if err != nil {
		t.Fatalf("first compile: %v", err)
	}
	byBase := diagsByBase(diags)
	if _, ok := byBase["warn.ex"]; !ok {
		t.Errorf("expected a diagnostic for warn.ex, got %v", baseNames(diags))
	}
	if _, ok := byBase["err.ex"]; !ok {
		t.Errorf("expected a diagnostic for err.ex, got %v", baseNames(diags))
	}

	// The isolated build must live under .dexter/build, not the default _build.
	if _, err := os.Stat(filepath.Join(root, ".dexter", "build", "dev")); err != nil {
		t.Errorf("expected build under .dexter/build/dev: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "_build")); err == nil {
		t.Errorf("compilation leaked into default _build directory")
	}

	// Fix the warning; recompile in the same VM. Sleep so the edited file's
	// mtime clearly advances (sub-second edits are not always detected).
	time.Sleep(1100 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(root, "lib", "warn.ex"),
		[]byte("defmodule DiagFixture.Warn do\n  def run, do: :ok\nend\n"), 0644); err != nil {
		t.Fatal(err)
	}

	diags, err = server.runProjectCompile(ctx, root)
	if err != nil {
		t.Fatalf("second compile: %v", err)
	}
	byBase = diagsByBase(diags)
	if _, ok := byBase["warn.ex"]; ok {
		t.Errorf("warn.ex diagnostic should be cleared after fix, got %v", baseNames(diags))
	}
	if _, ok := byBase["err.ex"]; !ok {
		t.Errorf("err.ex diagnostic should persist, got %v", baseNames(diags))
	}
}

func TestSyntaxCheck_ViaBeam(t *testing.T) {
	mixPath, err := exec.LookPath("mix")
	if err != nil {
		t.Skip("mix not available in PATH")
	}

	root := t.TempDir()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(st, root)
	server.mixBin = mixPath
	t.Cleanup(func() {
		server.closeBeams()
		_ = st.Close()
	})

	ctx := context.Background()

	// A buffer with a missing terminator reports an error at the opening line.
	res, err := server.runSyntaxCheck(ctx, root, "defmodule A do\n  def x do\n    :ok\n  end\n")
	if err != nil {
		t.Fatalf("syntax check (bad): %v", err)
	}
	if res.ok {
		t.Fatal("expected a syntax error for an unterminated module")
	}
	if res.line == 0 || res.message == "" {
		t.Errorf("expected line and message, got line=%d message=%q", res.line, res.message)
	}

	// A valid buffer parses cleanly.
	res, err = server.runSyntaxCheck(ctx, root, "defmodule Ok do\n  def x, do: :ok\nend\n")
	if err != nil {
		t.Fatalf("syntax check (good): %v", err)
	}
	if !res.ok {
		t.Errorf("expected clean parse, got error %q", res.message)
	}
}

func diagsByBase(diags []compileDiag) map[string]compileDiag {
	out := make(map[string]compileDiag)
	for _, d := range diags {
		out[filepath.Base(d.file)] = d
	}
	return out
}

func baseNames(diags []compileDiag) []string {
	var out []string
	for _, d := range diags {
		out = append(out, filepath.Base(d.file))
	}
	return out
}
