# PR #75 HEEX validation branch

This branch is a deliberately failing acceptance suite for PR #75. It contains
the standalone black-box LSP harness from `test/lsp-integration-harness`, the
exact PR head, and HEEX-specific tests without any proposed fixes.

Branch layering:

1. `main`
2. `test/lsp-integration-harness` — reusable, passing harness infrastructure
3. PR #75 head `713f6f37a8fe1ec1c1277da794e25de6b8b129be`
4. `test/pr75-heex-validation` — fixture, acceptance tests, regressions, and benchmarks

The generic harness tests remain green. The `TestPR75...` tests are expected to
fail until the corresponding PR issues are addressed.

## Run the black-box LSP acceptance suite

```sh
go test ./internal/lsp \
  -run 'TestPR75HEEXValidation|TestPR75HEEXFixtureCompiles' \
  -count=1 -v
```

Each behavior is a named subtest, so failures in one area do not hide the rest.
The test builds the real Dexter binary, copies and indexes the fixture, starts
`dexter lsp` over stdio, opens documents, and exercises definition, references,
highlights, rename, and unsaved `didChange` buffers.

## Run parser and tree regressions

```sh
go test ./internal/parser ./internal/treesitter \
  -run TestPR75Validation \
  -count=1 -v
```

## Run performance benchmarks

```sh
go test ./internal/parser \
  -run '^$' -bench '^BenchmarkPR75' -benchmem

go test ./internal/treesitter \
  -run '^$' -bench '^BenchmarkPR75' -benchmem
```

At 800 repeated expressions, the unmodified PR allocates approximately:

- HEEX tokenization: 78.4 MB/op
- Variable occurrence lookup: 45.2 MB/op

The static-HTML control benchmark is included to ensure optimizations do not
trade the expression regression for a large static-template regression.

## Acceptance coverage

The suite covers:

- nested braces containing maps and calls after the nested close;
- dynamic attributes on normal and self-closing tags;
- multiline interpolation line-start accounting;
- incomplete quoted, bracketed, and heredoc HEEX sigils;
- local components with and without unrelated imports or `use` injectors;
- inline and block function templates;
- local and remote component definitions, highlights, and references;
- tree-sitter-heex `function` and `module` node kinds;
- script and style literal braces, including JavaScript `<` operators;
- exact and nested `phx-no-curly-interpolation` behavior;
- EEx that remains active inside script, style, and no-curly regions;
- attribute expressions on raw tags;
- no-curly text appearing only inside an attribute value;
- HEEX assigns remaining distinct from Elixir module attributes;
- EEx `for` binding lifetime and outer-variable shadowing;
- isolation between case/fn-style arrow clauses;
- `:for` and `:let` binding lifetime;
- `#` inside an EEx string before a block-opening `do`;
- unsaved `didChange` requests with incomplete HEEX source;
- dependency-free Mix fixture compilation.

The UTF-16 cursor test after an emoji is retained as a skipped, explicitly
pre-existing issue. It is not a PR #75 acceptance blocker and should be handled
in separate LSP position-encoding work.

## Validate against real Phoenix LiveView

The fast protocol fixture uses a local `Phoenix.Component` stub so it stays
dependency-free. A second fixture is compiled and rendered by the real Phoenix
LiveView HEEX engine:

```sh
cd internal/lsp/testdata/phoenix_heex_app
mix deps.get
mix test
```

Its lockfile currently resolves Phoenix LiveView 1.2.6 and Phoenix 1.8.9. This
fixture verifies that the dynamic attributes, nested expressions, components,
EEx blocks, special attributes, raw script contents, no-curly contents, Unicode,
and string-hash loop examples are valid production HEEX syntax.

## Success criteria

- The generic `TestProtocolEndToEnd` harness remains green.
- All non-skipped `TestPR75...` tests pass.
- The fixture continues to compile.
- Repeated-expression allocation growth is no longer quadratic.
- Static-template throughput and allocation counts remain close to the control baseline.
