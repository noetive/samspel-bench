VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"
BINARY  := samspel

.PHONY: build test lint bench clean hooks

# The hooks are the only part of setup that lives in .git/config instead of the
# tree, which makes them the only part that can silently not be there. Wired
# into build and test because those are the first things anyone runs. Writing to
# .git/config is a real side effect, so it says so, and says nothing on the
# runs where it changes nothing.
#
# The probe compares the repository root to the working directory, not merely
# "am I inside a repository": a tarball unpacked anywhere inside an unrelated
# checkout would otherwise repoint *that* repository's core.hooksPath at a
# .githooks it does not have, silently disabling its hooks.
hooks:
	@test "$$(git rev-parse --show-toplevel 2>/dev/null)" = "$$(pwd -P)" || exit 0; \
	 test "$$(git config --get core.hooksPath)" = .githooks || { \
	   git config core.hooksPath .githooks && \
	   echo "hooks: core.hooksPath -> .githooks"; \
	 }

build: hooks
	go build $(LDFLAGS) -o bin/$(BINARY) ./cmd/$(BINARY)

# The cache is cleared first so a result is never a replay of an earlier run.
test: hooks
	go clean -testcache
	go test -race -count=1 ./...

lint:
	golangci-lint run --config ./.golangci.yml ./...

# Allocation counts. The figures stay local because this repository is public:
# keep them in an untracked BENCH_TRACKER.md, which .gitignore already holds.
bench:
	go test -run "^$$" -bench=. -benchmem ./...

clean:
	rm -rf bin/ tmp/ mem.out cpu.out
	@find . -name '*.test' -type f -print -exec rm -f {} +
