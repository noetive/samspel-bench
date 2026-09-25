# Contributing

## Getting set up

```sh
make hooks
make build test lint
```

`make hooks` points `core.hooksPath` at `.githooks`. You need a Go toolchain matching go.mod, and golangci-lint v2.5.0:

```sh
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.5.0
```

## Things that are load-bearing

**Hooks live in `.githooks`, and `make build`/`make test` wire them.** A fresh clone gets them from the first thing it runs.

**Pre-commit refuses credentials, large files, binaries, and unformatted Go.** The credential, size and binary checks read what you staged; the formatting check reads your working tree. It is fast on purpose so nobody reaches for `--no-verify`.

**Pre-push runs race tests and the linter.** That is the slow half; it runs at push frequency, not commit frequency.

**CI runs build, race tests, a `go mod tidy` check, govulncheck, and golangci-lint.** A hook you skip locally still meets all five before a change lands.

**Tests must not depend on the wall clock.** Derive any time-relative fixture from `time.Now()` or an injected clock, so a test that passes today still passes next year.

**Anything that parses untrusted input gets a fuzz target.** Agent-to-agent messages are untrusted input by definition.

## Commits

Changelog style: a `scope: imperative summary` subject under about 72 characters, then a short body describing what changed and the consequences a reader needs. Leave out narration of the work itself; the diff already carries that.

## Reporting a vulnerability

See [SECURITY.md](SECURITY.md).
