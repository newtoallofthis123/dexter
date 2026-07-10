# LSP integration tests

`internal/testutil/lspclient` is a black-box client for Dexter's stdio LSP. It
builds and starts the real binary, performs the initialize handshake, opens and
changes documents, and exposes typed helpers for definition, references,
document highlights, and rename.

The end-to-end test copies the dependency-free Mix project in
`internal/lsp/testdata/integration_app` to a temporary directory. It creates a
fresh Dexter index before starting the server, so CLI indexing and LSP lookup
are tested together without modifying the checked-in fixture.

Run the protocol test with:

```sh
go test ./internal/lsp -run TestProtocolEndToEnd -v
```

Run the fixture compilation check with:

```sh
go test ./internal/lsp -run TestIntegrationFixtureCompiles -v
```

To add a protocol scenario:

1. Put representative Elixir in `internal/lsp/testdata/integration_app`.
2. Open the file with `Client.Open` in `protocol_integration_test.go`.
3. Locate the request position with `Document.Position`; it converts source
   offsets to the UTF-16 positions required by LSP.
4. Assert the response through a typed client method. Prefer checking the
   target file and source line over hard-coding a complete location range.

Use `Options{DisableMix: true}` for tests that do not exercise formatting or
BEAM-backed features. This keeps protocol startup and shutdown deterministic.

Dexter currently treats LSP character offsets as UTF-8 byte columns while the
protocol defaults to UTF-16. Keep baseline fixtures ASCII-only until position
encoding is negotiated or converted correctly; add a Unicode protocol
regression as part of that fix.
