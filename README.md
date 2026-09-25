# samspel-bench

[![ci](https://github.com/noetive/samspel-bench/actions/workflows/ci.yml/badge.svg)](https://github.com/noetive/samspel-bench/actions/workflows/ci.yml)

Measure how well your agents work together, not only how well each one works alone.

samspel-bench is a benchmark for agent-to-agent collaboration. *Samspel* is Swedish for interplay.

## Status

The benchmark tasks and scoring aren't built yet. Today this repository holds the Go module, a `samspel` command that reports its version, and the checks every change has to pass.

## Development

You need Go (the version is in `go.mod`) and [golangci-lint](https://golangci-lint.run) v2.5.0.

```sh
make build   # compile bin/samspel and point git at the repository's hooks
make test    # race-enabled tests with a cleared cache
make lint    # golangci-lint with the repository's config
make bench   # allocation benchmarks, results stay on your machine
make clean   # remove build and scratch output
```

The hooks in `.githooks/` refuse credentials, large files, binaries and unformatted Go at commit time, and run tests and lint before a push. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Security

Report a vulnerability by email, not in a public issue. See [SECURITY.md](SECURITY.md).

## License

[Apache-2.0](LICENSE)
