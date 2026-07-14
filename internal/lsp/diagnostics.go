package lsp

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

//go:embed diag_server.exs
var diagServerScript string

const (
	// Service/op tags inside the dedicated diagnostics BEAM process. Its
	// service space is independent of the shared beam_server.exs, so 0x00 is
	// free to reuse here.
	serviceDiag   byte = 0x00
	diagOpCompile byte = 0x00

	// New op on the shared CodeIntel service (beam_server.exs) for Tier A.
	codeIntelOpSyntaxCheck byte = 0x05

	// Diagnostic sources kept per URI so Tier A (syntax) and Tier C (compile)
	// and formatter diagnostics don't clobber one another on publish.
	diagSourceSyntax  = "syntax"
	diagSourceCompile = "compile"
	diagSourceFormat  = "format"

	syntaxDebounce     = 250 * time.Millisecond
	diagCompileTimeout = 120 * time.Second
	// Cold compiles build the project (and its deps) into an empty
	// .dexter/build; on large projects that legitimately takes many minutes.
	diagColdCompileTimeout = 20 * time.Minute
	diagIdleTimeout        = 30 * time.Minute
)

// diagPublishOrder fixes the order sources are flattened in so published
// diagnostics are deterministic regardless of Go's map iteration order.
var diagPublishOrder = []string{diagSourceSyntax, diagSourceCompile, diagSourceFormat}

// compileDiag is a single decoded diagnostic from the diagnostics BEAM. Lines
// and columns are 1-based as Mix reports them; 0 means "unknown".
type compileDiag struct {
	severity                             byte
	startLine, startCol, endLine, endCol uint32
	file                                 string
	message                              string
	compiler                             string
}

// syntaxResult is the outcome of a Tier A string_to_quoted check.
type syntaxResult struct {
	ok      bool
	line    uint32
	col     uint32
	message string
}

// === Diagnostic store: merge sources per URI ===

// diagnosticStore holds the current diagnostics for each document, split by
// source, and publishes the flattened union whenever any source changes. This
// keeps syntax, compile, and formatter diagnostics from overwriting each other
// (they all target textDocument/publishDiagnostics, which is a full replace).
type diagnosticStore struct {
	client func() protocol.Client

	mu    sync.Mutex
	byURI map[protocol.DocumentURI]map[string][]protocol.Diagnostic
}

func newDiagnosticStore(client func() protocol.Client) *diagnosticStore {
	return &diagnosticStore{
		client: client,
		byURI:  make(map[protocol.DocumentURI]map[string][]protocol.Diagnostic),
	}
}

// set replaces the diagnostics for one source of one URI and republishes the
// union of all sources. Passing an empty slice clears that source.
func (d *diagnosticStore) set(docURI protocol.DocumentURI, source string, diags []protocol.Diagnostic) {
	d.mu.Lock()
	sources := d.byURI[docURI]
	if sources == nil {
		if len(diags) == 0 {
			d.mu.Unlock()
			// Nothing was published for this URI and nothing is now — but the
			// document may have had diagnostics from a previous session view;
			// publishing an empty array is cheap and idempotent, so fall
			// through to publish below.
			d.publish(docURI, nil)
			return
		}
		sources = make(map[string][]protocol.Diagnostic)
		d.byURI[docURI] = sources
	}

	if len(diags) == 0 {
		delete(sources, source)
	} else {
		sources[source] = diags
	}

	union := make([]protocol.Diagnostic, 0)
	for _, src := range diagPublishOrder {
		union = append(union, sources[src]...)
	}
	if len(sources) == 0 {
		delete(d.byURI, docURI)
	}
	d.mu.Unlock()

	d.publish(docURI, union)
}

func (d *diagnosticStore) publish(docURI protocol.DocumentURI, diags []protocol.Diagnostic) {
	client := d.client()
	if client == nil {
		return
	}
	if diags == nil {
		diags = []protocol.Diagnostic{}
	}
	_ = client.PublishDiagnostics(context.Background(), &protocol.PublishDiagnosticsParams{
		URI:         docURI,
		Diagnostics: diags,
	})
}

// === Mapping ===

func mapDiagnosticSeverity(sev byte) protocol.DiagnosticSeverity {
	switch sev {
	case 0:
		return protocol.DiagnosticSeverityError
	case 1:
		return protocol.DiagnosticSeverityWarning
	case 2:
		return protocol.DiagnosticSeverityInformation
	case 3:
		return protocol.DiagnosticSeverityHint
	default:
		return protocol.DiagnosticSeverityWarning
	}
}

// zeroBasedLine converts a 1-based Mix line/column to LSP's 0-based form. A 0
// value means Mix didn't report it, which maps to 0 (start of line).
func zeroBased(n uint32) uint32 {
	if n > 0 {
		return n - 1
	}
	return 0
}

func mapCompileDiagnostic(d compileDiag) protocol.Diagnostic {
	startLine := zeroBased(d.startLine)
	startCol := zeroBased(d.startCol)
	endLine := startLine
	endCol := startCol
	if d.endLine > 0 {
		endLine = zeroBased(d.endLine)
	}
	if d.endCol > 0 {
		endCol = zeroBased(d.endCol)
	}
	if endLine < startLine || (endLine == startLine && endCol < startCol) {
		endLine, endCol = startLine, startCol
	}

	return protocol.Diagnostic{
		Range: protocol.Range{
			Start: protocol.Position{Line: startLine, Character: startCol},
			End:   protocol.Position{Line: endLine, Character: endCol},
		},
		Severity: mapDiagnosticSeverity(d.severity),
		Source:   "dexter",
		Message:  d.message,
	}
}

func syntaxDiagnostic(res *syntaxResult) protocol.Diagnostic {
	line := zeroBased(res.line)
	col := zeroBased(res.col)
	return protocol.Diagnostic{
		Range: protocol.Range{
			Start: protocol.Position{Line: line, Character: col},
			End:   protocol.Position{Line: line, Character: col},
		},
		Severity: protocol.DiagnosticSeverityError,
		Source:   "dexter",
		Message:  res.message,
	}
}

// === Tier A: syntax checker (debounced, version-guarded) ===

type syntaxChecker struct {
	debounce time.Duration
	check    func(ctx context.Context, buildRoot, content string) (*syntaxResult, error)
	publish  func(docURI protocol.DocumentURI, diags []protocol.Diagnostic)
	debugf   func(string, ...interface{})

	mu       sync.Mutex
	timers   map[string]*time.Timer
	versions map[string]int32
}

func newSyntaxChecker(
	debounce time.Duration,
	check func(ctx context.Context, buildRoot, content string) (*syntaxResult, error),
	publish func(docURI protocol.DocumentURI, diags []protocol.Diagnostic),
	debugf func(string, ...interface{}),
) *syntaxChecker {
	return &syntaxChecker{
		debounce: debounce,
		check:    check,
		publish:  publish,
		debugf:   debugf,
		timers:   make(map[string]*time.Timer),
		versions: make(map[string]int32),
	}
}

// schedule records the latest version for a document and (re)arms the debounce
// timer. A newer change cancels the pending older one.
func (c *syntaxChecker) schedule(docURI string, version int32, content, buildRoot string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.versions[docURI] = version
	if t := c.timers[docURI]; t != nil {
		t.Stop()
	}
	c.timers[docURI] = time.AfterFunc(c.debounce, func() {
		c.fire(docURI, version, content, buildRoot)
	})
}

func (c *syntaxChecker) fire(docURI string, version int32, content, buildRoot string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.check(ctx, buildRoot, content)
	if err != nil {
		c.debugf("diagnostics: syntax check failed for %s: %v", docURI, err)
		return
	}

	// Version guard: drop the result if the buffer moved on while we waited.
	c.mu.Lock()
	current := c.versions[docURI]
	c.mu.Unlock()
	if current != version {
		return
	}

	if res.ok {
		c.publish(protocol.DocumentURI(docURI), nil)
		return
	}
	c.publish(protocol.DocumentURI(docURI), []protocol.Diagnostic{syntaxDiagnostic(res)})
}

// clear cancels any pending check and clears the document's syntax diagnostics.
func (c *syntaxChecker) clear(docURI string) {
	c.mu.Lock()
	if t := c.timers[docURI]; t != nil {
		t.Stop()
		delete(c.timers, docURI)
	}
	delete(c.versions, docURI)
	c.mu.Unlock()
	c.publish(protocol.DocumentURI(docURI), nil)
}

func (c *syntaxChecker) shutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for uri, t := range c.timers {
		t.Stop()
		delete(c.timers, uri)
	}
}

// === Tier C: compile manager (single-flight, publish-diff, idle) ===

type diagProject struct {
	compiling bool
	dirty     bool
	cold      bool // true until the first successful compile completes
	notified  bool // a failure notification was shown; reset on success
	lastFiles map[protocol.DocumentURI]bool
	idleTimer *time.Timer
}

type diagManager struct {
	compile         func(ctx context.Context, root string) ([]compileDiag, error)
	setCompileDiags func(docURI protocol.DocumentURI, diags []protocol.Diagnostic)
	kill            func(root string) // release the project's process (nil ok)
	onCold          func(root string) func()
	notify          func(msg string) // user-visible failure notice (nil ok)
	debugf          func(string, ...interface{})
	timeout         time.Duration
	coldTimeout     time.Duration
	idle            time.Duration

	mu       sync.Mutex
	projects map[string]*diagProject
	stopped  bool
}

func newDiagManager(
	compile func(ctx context.Context, root string) ([]compileDiag, error),
	setCompileDiags func(docURI protocol.DocumentURI, diags []protocol.Diagnostic),
	kill func(root string),
	onCold func(root string) func(),
	notify func(msg string),
	debugf func(string, ...interface{}),
) *diagManager {
	return &diagManager{
		compile:         compile,
		setCompileDiags: setCompileDiags,
		kill:            kill,
		onCold:          onCold,
		notify:          notify,
		debugf:          debugf,
		timeout:         diagCompileTimeout,
		coldTimeout:     diagColdCompileTimeout,
		idle:            diagIdleTimeout,
		projects:        make(map[string]*diagProject),
	}
}

// trigger requests a compile for a project root. If one is already running, it
// sets a dirty flag so exactly one more compile runs after the current one.
func (m *diagManager) trigger(root string) {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	p := m.projects[root]
	if p == nil {
		p = &diagProject{cold: true, lastFiles: make(map[protocol.DocumentURI]bool)}
		m.projects[root] = p
	}
	m.resetIdleLocked(root, p)

	if p.compiling {
		p.dirty = true
		m.mu.Unlock()
		return
	}
	p.compiling = true
	m.mu.Unlock()

	go m.run(root, p)
}

func (m *diagManager) run(root string, p *diagProject) {
	for {
		m.mu.Lock()
		cold := p.cold
		m.mu.Unlock()

		var endProgress func()
		if cold && m.onCold != nil {
			endProgress = m.onCold(root)
		}

		timeout := m.timeout
		if cold {
			timeout = m.coldTimeout
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		diags, err := m.compile(ctx, root)
		cancel()

		if endProgress != nil {
			endProgress()
		}

		if err != nil {
			// Timeout or crash: drop the round and release the process so the
			// next save starts a fresh one. Tell the user once — silent failure
			// looks like diagnostics simply don't work.
			m.debugf("diagnostics: compile failed for %s: %v", root, err)
			m.mu.Lock()
			firstFailure := !p.notified
			p.notified = true
			m.mu.Unlock()
			if firstFailure && m.notify != nil {
				m.notify(fmt.Sprintf("dexter: compile diagnostics for %s failed: %v", filepath.Base(root), err))
			}
			if m.kill != nil {
				m.kill(root)
			}
		} else {
			m.publishResult(root, p, diags)
			m.mu.Lock()
			p.cold = false
			p.notified = false
			m.mu.Unlock()
		}

		m.mu.Lock()
		if p.dirty && !m.stopped {
			p.dirty = false
			m.mu.Unlock()
			continue
		}
		p.compiling = false
		m.mu.Unlock()
		return
	}
}

// publishResult publishes the current diagnostics grouped by file and clears
// files that had diagnostics last round but are clean now.
func (m *diagManager) publishResult(root string, p *diagProject, diags []compileDiag) {
	grouped := make(map[protocol.DocumentURI][]protocol.Diagnostic)
	for _, d := range diags {
		if d.file == "" {
			continue
		}
		docURI := protocol.DocumentURI(uri.File(d.file))
		grouped[docURI] = append(grouped[docURI], mapCompileDiagnostic(d))
	}

	m.mu.Lock()
	prev := p.lastFiles
	next := make(map[protocol.DocumentURI]bool, len(grouped))
	for docURI := range grouped {
		next[docURI] = true
	}
	var cleared []protocol.DocumentURI
	for docURI := range prev {
		if !next[docURI] {
			cleared = append(cleared, docURI)
		}
	}
	p.lastFiles = next
	m.mu.Unlock()

	for docURI, ds := range grouped {
		m.setCompileDiags(docURI, ds)
	}
	for _, docURI := range cleared {
		m.setCompileDiags(docURI, nil)
	}
}

// resetIdleLocked arms (or re-arms) the idle timer that releases the project's
// process after a period with no saves. Caller holds m.mu.
func (m *diagManager) resetIdleLocked(root string, p *diagProject) {
	if m.idle <= 0 {
		return
	}
	if p.idleTimer != nil {
		p.idleTimer.Stop()
	}
	p.idleTimer = time.AfterFunc(m.idle, func() {
		m.debugf("diagnostics: project %s idle, releasing process", root)
		if m.kill != nil {
			m.kill(root)
		}
	})
}

func (m *diagManager) shutdown() {
	m.mu.Lock()
	m.stopped = true
	for _, p := range m.projects {
		if p.idleTimer != nil {
			p.idleTimer.Stop()
		}
	}
	m.mu.Unlock()
}

// === BEAM wire methods ===

// SyntaxCheck asks the shared CodeIntel service to parse a buffer and report
// the first syntax error (Tier A). It is pure and stateless on the BEAM side.
func (bp *beamProcess) SyntaxCheck(ctx context.Context, content string) (*syntaxResult, error) {
	var result *syntaxResult
	var payload bytes.Buffer
	_ = binary.Write(&payload, binary.BigEndian, uint32(len(content)))
	payload.WriteString(content)

	err := bp.doRequest(ctx, serviceCodeIntel, codeIntelOpSyntaxCheck, payload.Bytes(), func(status byte, payload []byte) error {
		if status != 0 {
			return fmt.Errorf("syntax check failed: %s", strings.TrimSpace(string(payload)))
		}
		reader := bytes.NewReader(payload)
		okFlag, err := readByte(reader)
		if err != nil {
			return fmt.Errorf("read ok flag: %w", err)
		}
		if okFlag == 1 {
			result = &syntaxResult{ok: true}
			return nil
		}
		line, err := readUint32(reader)
		if err != nil {
			return fmt.Errorf("read line: %w", err)
		}
		col, err := readUint32(reader)
		if err != nil {
			return fmt.Errorf("read column: %w", err)
		}
		msgLen, err := readUint32(reader)
		if err != nil {
			return fmt.Errorf("read message length: %w", err)
		}
		msg, err := readPayload(reader, msgLen)
		if err != nil {
			return fmt.Errorf("read message: %w", err)
		}
		result = &syntaxResult{ok: false, line: line, col: col, message: string(msg)}
		return nil
	})
	return result, err
}

// Compile asks the dedicated diagnostics BEAM to compile its project and return
// the full current diagnostic set (Tier C).
func (bp *beamProcess) Compile(ctx context.Context) ([]compileDiag, error) {
	var diags []compileDiag
	err := bp.doRequest(ctx, serviceDiag, diagOpCompile, nil, func(status byte, payload []byte) error {
		if status != 0 {
			return fmt.Errorf("compile failed: %s", strings.TrimSpace(string(payload)))
		}
		reader := bytes.NewReader(payload)
		count, err := readUint32(reader)
		if err != nil {
			return fmt.Errorf("read diagnostic count: %w", err)
		}
		diags = make([]compileDiag, 0, count)
		for i := uint32(0); i < count; i++ {
			d, err := readCompileDiag(reader)
			if err != nil {
				return fmt.Errorf("read diagnostic %d: %w", i, err)
			}
			diags = append(diags, d)
		}
		return nil
	})
	return diags, err
}

func readCompileDiag(reader io.Reader) (compileDiag, error) {
	var d compileDiag
	sev, err := readByte(reader)
	if err != nil {
		return d, err
	}
	d.severity = sev
	for _, field := range []*uint32{&d.startLine, &d.startCol, &d.endLine, &d.endCol} {
		v, err := readUint32(reader)
		if err != nil {
			return d, err
		}
		*field = v
	}
	fileLen, err := readUint32(reader)
	if err != nil {
		return d, err
	}
	file, err := readPayload(reader, fileLen)
	if err != nil {
		return d, err
	}
	d.file = string(file)
	msgLen, err := readUint32(reader)
	if err != nil {
		return d, err
	}
	msg, err := readPayload(reader, msgLen)
	if err != nil {
		return d, err
	}
	d.message = string(msg)
	var compLen uint16
	if err := binary.Read(reader, binary.BigEndian, &compLen); err != nil {
		return d, err
	}
	comp, err := readPayload(reader, uint32(compLen))
	if err != nil {
		return d, err
	}
	d.compiler = string(comp)
	return d, nil
}

// === Dedicated diagnostics process lifecycle ===

// startDiagProcess launches a diagnostics BEAM for a mix project root. Like
// startBeamProcess it returns immediately; the process may not be ready yet.
func (s *Server) startDiagProcess(mixRoot string) (*beamProcess, error) {
	scriptDir := filepath.Join(os.TempDir(), "dexter")
	if err := os.MkdirAll(scriptDir, 0755); err != nil {
		return nil, fmt.Errorf("create script dir: %w", err)
	}
	scriptPath := filepath.Join(scriptDir, "diag_server.exs")
	if existing, err := os.ReadFile(scriptPath); err != nil || string(existing) != diagServerScript {
		if err := os.WriteFile(scriptPath, []byte(diagServerScript), 0644); err != nil {
			return nil, fmt.Errorf("write diag server script: %w", err)
		}
	}

	elixirBin := filepath.Join(filepath.Dir(s.mixBin), "elixir")
	cmd := exec.Command(elixirBin, scriptPath, mixRoot)
	cmd.Dir = mixRoot
	buildRoot := filepath.Join(mixRoot, ".dexter", "build")
	cmd.Env = append(os.Environ(), "MIX_ENV=dev", "MIX_BUILD_ROOT="+buildRoot)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderrBuf := newStderrCapture()
	cmd.Stderr = io.MultiWriter(os.Stderr, stderrBuf)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start diag BEAM: %w", err)
	}

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	bp := &beamProcess{
		cmd:       &commandHandle{process: cmd.Process, done: done},
		stdin:     stdin,
		stdout:    stdout,
		stderr:    stderrBuf,
		pending:   make(map[uint32]chan beamResponse),
		startedAt: time.Now(),
		ready:     make(chan struct{}),
		closed:    make(chan struct{}),
	}

	go func() {
		select {
		case <-bp.ready:
			if bp.startErr != nil {
				_ = cmd.Process.Kill()
				<-done
			}
		case <-time.After(beamStuckTimeout):
			bp.finishStartup(fmt.Errorf("diag BEAM startup timed out"))
			_ = cmd.Process.Kill()
			<-done
		}
	}()

	go bp.readLoop()

	return bp, nil
}

// getDiagProcess returns the diagnostics process for a mix root, starting one
// if needed. Never shares with the formatter/CodeIntel BEAM.
func (s *Server) getDiagProcess(ctx context.Context, mixRoot string) *beamProcess {
	s.diagBeamMu.Lock()
	defer s.diagBeamMu.Unlock()

	if bp, ok := s.diagBeams[mixRoot]; ok {
		if bp.alive() {
			return bp
		}
		log.Printf("diag BEAM: process for %s is dead, restarting", mixRoot)
	}

	if s.mixBin == "" {
		return nil
	}

	bp, err := s.startDiagProcess(mixRoot)
	if err != nil {
		log.Printf("diag BEAM: failed to start for %s: %v", mixRoot, err)
		return nil
	}
	if s.diagBeams == nil {
		s.diagBeams = make(map[string]*beamProcess)
	}
	s.diagBeams[mixRoot] = bp
	return bp
}

// stopDiagProcess kills and forgets the diagnostics process for a mix root.
func (s *Server) stopDiagProcess(mixRoot string) {
	s.diagBeamMu.Lock()
	bp := s.diagBeams[mixRoot]
	delete(s.diagBeams, mixRoot)
	s.diagBeamMu.Unlock()

	if bp != nil {
		bp.closeWithReason("diag process released (idle or failed)")
	}
}

func (s *Server) closeDiagBeams() {
	s.diagBeamMu.Lock()
	defer s.diagBeamMu.Unlock()
	for _, bp := range s.diagBeams {
		bp.closeWithReason("server shutdown")
	}
	s.diagBeams = nil
}

// runProjectCompile is the diagManager's production compile function.
func (s *Server) runProjectCompile(ctx context.Context, mixRoot string) ([]compileDiag, error) {
	bp := s.getDiagProcess(ctx, mixRoot)
	if bp == nil {
		return nil, fmt.Errorf("diag process unavailable")
	}
	if err := bp.Ready(ctx); err != nil {
		return nil, err
	}
	return bp.Compile(ctx)
}

// runSyntaxCheck is the syntaxChecker's production check function. It uses the
// shared BEAM (same instance as the formatter) since string_to_quoted is pure.
func (s *Server) runSyntaxCheck(ctx context.Context, buildRoot, content string) (*syntaxResult, error) {
	bp := s.getBeamProcess(ctx, buildRoot)
	if bp == nil {
		return nil, fmt.Errorf("beam process unavailable")
	}
	if err := bp.Ready(ctx); err != nil {
		return nil, err
	}
	return bp.SyntaxCheck(ctx, content)
}

// startCompileProgress reports a WorkDoneProgress for a cold compile if the
// client advertised support. Returns a no-op end function otherwise.
func (s *Server) startCompileProgress(root string) func() {
	noop := func() {}
	if !s.workDoneProgressSupported || s.client == nil {
		return noop
	}

	token := protocol.NewProgressToken(fmt.Sprintf("dexter-compile-%d", time.Now().UnixNano()))
	ctx := context.Background()
	if err := s.client.WorkDoneProgressCreate(ctx, &protocol.WorkDoneProgressCreateParams{Token: *token}); err != nil {
		return noop
	}
	_ = s.client.Progress(ctx, &protocol.ProgressParams{
		Token: *token,
		Value: &protocol.WorkDoneProgressBegin{
			Kind:  protocol.WorkDoneProgressKindBegin,
			Title: fmt.Sprintf("dexter: compiling %s", filepath.Base(root)),
		},
	})

	// Cold compiles can run for minutes; report elapsed time so the progress
	// UI visibly stays alive instead of looking hung.
	started := time.Now()
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				elapsed := time.Since(started).Round(time.Second)
				_ = s.client.Progress(ctx, &protocol.ProgressParams{
					Token: *token,
					Value: &protocol.WorkDoneProgressReport{
						Kind:    protocol.WorkDoneProgressKindReport,
						Message: fmt.Sprintf("cold build, %s elapsed", elapsed),
					},
				})
			}
		}
	}()

	return func() {
		close(stop)
		_ = s.client.Progress(ctx, &protocol.ProgressParams{
			Token: *token,
			Value: &protocol.WorkDoneProgressEnd{Kind: protocol.WorkDoneProgressKindEnd},
		})
	}
}
