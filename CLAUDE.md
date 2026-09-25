# samspel-bench

A benchmark for agent-to-agent collaboration, written in Go.

## This repository is public

Everything tracked here is outbound: code comments, test names and fixtures, commit
messages, this file. Anyone can clone it.

- Write every word under the `noetive-messaging` skill and filter it through
  `noetive-confidential`. No internal hostnames, account numbers, customer or partner
  names, roadmap dates, or descriptions of how Noetive services work internally.
- A comment that assumes only the team reads it is the usual leak. Describe what a check
  guarantees, not how some other service achieves it.
- Benchmark and allocation figures stay in an untracked `BENCH_TRACKER.md`. `.gitignore`
  already holds it.

## Building and testing

```
make build test lint
```

`make build` and `make test` point `core.hooksPath` at `.githooks/`. pre-commit is a fast
filter (credentials, size, binaries, formatting); pre-push runs race tests and lint. CI
runs build, race tests, `go mod tidy -diff`, govulncheck and golangci-lint on every push.

There are no dependencies yet. Vendor with the first one, so a build needs no network and
mutation testing can build mutants in an isolated copy of the module.

## JSON

Use `goccy/go-json` for encoding and `tidwall/gjson` for reading fields, never
`encoding/json`. Any path that decodes bytes a model or another agent produced is outside
the trust boundary and gets a fuzz target before it merges.

## Testing

- `go test -race -count=1 ./...`, always with both flags.
- Test behaviour and properties, not implementation. Each test carries a comment saying
  what it covers and why that behaviour matters.
- Anything that parses input from outside the trust boundary gets a fuzz target. A fuzzer
  asserts properties (never panics, whatever parses re-parses), not specific outputs.
- A corpus entry under `testdata/fuzz/` is a regression that was once real. Never delete
  one to make a suite pass.
- Tests must not break as time passes: derive every timestamp from `time.Now()` or an
  injected clock, never a hardcoded date.
- Verify a test by mutating the code it guards, with `github.com/gurre/mutest`. A survivor
  needs a better assertion; an unreached site needs a test. Say so when a survivor is
  genuinely equivalent rather than contorting a test to kill it.

## Naming and layout

- No `helpers`, `common`, `shared`, `utils`. Name a package after the domain concept it
  owns.
- Declare interfaces at the point of use, named as agentive nouns. Structs are common
  nouns.
- Commands live under `cmd/`. The binary is `samspel`.

## Logging

`log/slog` only. Never log a secret, a credential or an environment value.

## Commits

- Work on `main`. Never `git stash`; other agents may share the working tree.
- Changelog style: a `scope: imperative summary` subject under ~72 characters, then a short
  body of what changed and what a reader needs to know. Bullet the surface that moved.
- Never pass `--no-verify`. If a hook is wrong, fix the hook.
