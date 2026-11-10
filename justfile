set dotenv-load
export GOTOOLCHAIN := "local"
export GOPROXY := "off"
export GOSUMDB := "off"
export GOFLAGS := "-mod=readonly"

default:
    @just --list

# Run all package tests. Extra arguments go to go test.
[positional-arguments]
test *args:
    go test "$@" ./...

# Test with the minimum supported Go version.
test-min:
    nix develop .#go122 --command just test

test-race:
    go test -race -count=1 ./...

# Require the independent zpg protocol compatibility tests.
test-compat:
    ZPGO_REQUIRE_ZPG=1 go test -run '^TestZpgCompatibility$' -count=1 -timeout=60s ./...

# Limit the complete test process tree (Linux with a systemd user session).
[positional-arguments]
test-limited *args:
    systemd-run --user --scope --quiet -p MemoryMax=768M -p MemorySwapMax=0 -p TasksMax=96 env GOMAXPROCS=2 go test -p=2 -parallel=2 -timeout=60s "$@" ./...

coverage:
    go test -coverprofile=coverage.out ./...
    go tool cover -func=coverage.out

bench:
    go test -run='^$' -bench=. -benchmem ./...

build:
    go build ./...

check: lint typos fmt-check test-race

lint:
    go vet ./...
    deadnix --fail flake.nix
    statix check .
    markdownlint README.md

typos:
    typos

fmt-check:
    @files="$(gofmt -l .)"; if [ -n "$files" ]; then printf '%s\n' "$files"; exit 1; fi
    nixfmt --check flake.nix
    just --fmt --check

fmt:
    go fmt ./...
    nixfmt flake.nix
    just --fmt

hooks:
    pre-commit run --all-files

# Check both Go versions and all hooks in the Nix build sandbox.
check-nix:
    nix flake check

# Create a commit with a Conventional Commits message.
commit:
    cz commit
