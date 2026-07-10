// Package lspclient provides a black-box test client for Dexter's stdio LSP.
package lspclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
	"go.uber.org/zap"
)

type lockedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

type stdio struct {
	reader io.ReadCloser
	writer io.WriteCloser
}

func (s *stdio) Read(p []byte) (int, error)  { return s.reader.Read(p) }
func (s *stdio) Write(p []byte) (int, error) { return s.writer.Write(p) }
func (s *stdio) Close() error {
	writeErr := s.writer.Close()
	readErr := s.reader.Close()
	if writeErr != nil {
		return writeErr
	}
	return readErr
}

// Client owns a running Dexter process and a typed LSP server dispatcher.
type Client struct {
	root   string
	cmd    *exec.Cmd
	conn   jsonrpc2.Conn
	server protocol.Server
	stderr *lockedBuffer
	done   chan error
	cancel context.CancelFunc
}

// Index builds a fresh on-disk Dexter index for root using the real CLI.
func Index(ctx context.Context, binary, root string) error {
	cmd := exec.CommandContext(ctx, binary, "init", "--force", root)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("index fixture: %w\n%s", err, output)
	}
	return nil
}

// Options controls optional external processes used by the server under test.
type Options struct {
	// DisableMix keeps formatter and BEAM startup out of protocol tests that do
	// not exercise those features. It makes startup and shutdown deterministic.
	DisableMix bool
}

// Start launches `dexter lsp` over stdio and completes the initialize handshake.
func Start(ctx context.Context, binary, root string) (*Client, error) {
	return StartWithOptions(ctx, binary, root, Options{})
}

// StartWithOptions launches the server with the supplied test options.
func StartWithOptions(ctx context.Context, binary, root string, options Options) (*Client, error) {
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, binary, "lsp", root)
	cmd.Dir = root
	if options.DisableMix {
		for _, variable := range os.Environ() {
			if !strings.HasPrefix(variable, "PATH=") {
				cmd.Env = append(cmd.Env, variable)
			}
		}
		cmd.Env = append(cmd.Env, "PATH="+filepath.Dir(binary))
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}

	stream := jsonrpc2.NewStream(&stdio{reader: stdout, writer: stdin})
	conn := jsonrpc2.NewConn(stream)
	handler := func(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
		switch req.Method() {
		case protocol.MethodWorkspaceConfiguration:
			return reply(ctx, []interface{}{}, nil)
		case protocol.MethodWorkspaceWorkspaceFolders:
			return reply(ctx, []protocol.WorkspaceFolder{{
				URI: string(uri.File(root)), Name: filepath.Base(root),
			}}, nil)
		case protocol.MethodWorkspaceApplyEdit:
			return reply(ctx, true, nil)
		default:
			// Registration, log, progress, diagnostics, and show-message calls do
			// not need editor behavior in integration tests.
			return reply(ctx, nil, nil)
		}
	}
	conn.Go(ctx, handler)

	client := &Client{
		root: root, cmd: cmd, conn: conn,
		server: protocol.ServerDispatcher(conn, zap.NewNop()),
		stderr: stderr, done: make(chan error, 1), cancel: cancel,
	}
	go func() { client.done <- cmd.Wait() }()

	var initializationOptions interface{}
	if options.DisableMix {
		initializationOptions = map[string]interface{}{
			"stdlibPath": filepath.Join(root, ".missing-stdlib"),
		}
	}

	initCtx, initCancel := context.WithTimeout(ctx, 10*time.Second)
	defer initCancel()
	if _, err := client.server.Initialize(initCtx, &protocol.InitializeParams{
		ProcessID: int32(os.Getpid()),
		ClientInfo: &protocol.ClientInfo{
			Name: "dexter-integration-test",
		},
		RootURI:               protocol.DocumentURI(uri.File(root)),
		InitializationOptions: initializationOptions,
		WorkspaceFolders: []protocol.WorkspaceFolder{{
			URI: string(uri.File(root)), Name: filepath.Base(root),
		}},
		Capabilities: protocol.ClientCapabilities{},
	}); err != nil {
		client.forceStop()
		return nil, fmt.Errorf("initialize: %w\n%s", err, stderr.String())
	}
	if err := client.server.Initialized(initCtx, &protocol.InitializedParams{}); err != nil {
		client.forceStop()
		return nil, fmt.Errorf("initialized: %w\n%s", err, stderr.String())
	}
	return client, nil
}

// Document is an editor-owned fixture opened through textDocument/didOpen.
type Document struct {
	Path    string
	URI     protocol.DocumentURI
	Text    string
	Version int32
}

// Open reads and opens a fixture document through textDocument/didOpen.
func (c *Client) Open(ctx context.Context, relativePath string) (*Document, error) {
	path := relativePath
	if !filepath.IsAbs(path) {
		path = filepath.Join(c.root, relativePath)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc := &Document{
		Path: path, URI: protocol.DocumentURI(uri.File(path)),
		Text: string(data), Version: 1,
	}
	err = c.server.DidOpen(ctx, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: doc.URI, LanguageID: "elixir", Version: doc.Version, Text: doc.Text,
		},
	})
	return doc, err
}

// Change replaces the complete editor-owned document through textDocument/didChange.
func (c *Client) Change(ctx context.Context, doc *Document, text string) error {
	doc.Version++
	doc.Text = text
	return c.server.DidChange(ctx, &protocol.DidChangeTextDocumentParams{
		TextDocument: protocol.VersionedTextDocumentIdentifier{
			TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: doc.URI},
			Version:                doc.Version,
		},
		ContentChanges: []protocol.TextDocumentContentChangeEvent{{Text: text}},
	})
}

// Definition sends a textDocument/definition request.
func (c *Client) Definition(ctx context.Context, doc *Document, position protocol.Position) ([]protocol.Location, error) {
	return c.server.Definition(ctx, &protocol.DefinitionParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: doc.URI}, Position: position,
		},
	})
}

// References sends a textDocument/references request.
func (c *Client) References(ctx context.Context, doc *Document, position protocol.Position, includeDeclaration bool) ([]protocol.Location, error) {
	return c.server.References(ctx, &protocol.ReferenceParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: doc.URI}, Position: position,
		},
		Context: protocol.ReferenceContext{IncludeDeclaration: includeDeclaration},
	})
}

// Highlights sends a textDocument/documentHighlight request.
func (c *Client) Highlights(ctx context.Context, doc *Document, position protocol.Position) ([]protocol.DocumentHighlight, error) {
	return c.server.DocumentHighlight(ctx, &protocol.DocumentHighlightParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: doc.URI}, Position: position,
		},
	})
}

// Rename sends a textDocument/rename request.
func (c *Client) Rename(ctx context.Context, doc *Document, position protocol.Position, newName string) (*protocol.WorkspaceEdit, error) {
	return c.server.Rename(ctx, &protocol.RenameParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: doc.URI}, Position: position,
		},
		NewName: newName,
	})
}

// Position returns the start position of the nth zero-based occurrence of needle.
func (d *Document) Position(needle string, nth int) (protocol.Position, error) {
	if needle == "" || nth < 0 {
		return protocol.Position{}, fmt.Errorf("invalid needle occurrence")
	}
	from := 0
	index := -1
	for range nth + 1 {
		relative := strings.Index(d.Text[from:], needle)
		if relative < 0 {
			return protocol.Position{}, fmt.Errorf("%q occurrence %d not found in %s", needle, nth, d.Path)
		}
		index = from + relative
		from = index + len(needle)
	}
	prefix := d.Text[:index]
	line := strings.Count(prefix, "\n")
	lineStart := strings.LastIndexByte(prefix, '\n') + 1
	character := len(utf16.Encode([]rune(d.Text[lineStart:index])))
	return protocol.Position{Line: uint32(line), Character: uint32(character)}, nil
}

// Stderr returns the server process's stderr output collected so far.
func (c *Client) Stderr() string { return c.stderr.String() }

// Close performs the LSP shutdown/exit sequence and stops the process if needed.
func (c *Client) Close(ctx context.Context) error {
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	shutdownErr := c.server.Shutdown(shutdownCtx)
	_ = c.server.Exit(shutdownCtx)
	select {
	case <-c.done:
		c.cancel()
		return shutdownErr
	case <-shutdownCtx.Done():
		c.forceStop()
		if shutdownErr == nil {
			// Some servers acknowledge shutdown but do not terminate on exit.
			// The process has been force-stopped, so cleanup is complete.
			return nil
		}
		return fmt.Errorf("LSP did not exit: %w\n%s", shutdownCtx.Err(), c.stderr.String())
	}
}

func (c *Client) forceStop() {
	c.cancel()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}
