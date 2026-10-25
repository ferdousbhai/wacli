# wacli needs cgo: the WhatsApp session store is SQLite (go-sqlite3).
GO ?= go
export CGO_ENABLED := 1
# GCC 15+ rejects go-sqlite3's C without this.
export CGO_CFLAGS := $(CGO_CFLAGS) -Wno-error=missing-braces

GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.8.0
DEADCODE := golang.org/x/tools/cmd/deadcode@v0.52.0

.PHONY: build install test lint check

## build: compile dist/wacli
build:
	$(GO) build -o dist/wacli ./cmd/wacli

## install: install wacli into ~/.local/bin
install:
	GOBIN=$(HOME)/.local/bin $(GO) install ./cmd/wacli

## test: unit tests (race detector), Windows and 32-bit builds of the
## cgo-free packages, and the readable error for builds without cgo
test:
	$(GO) test -race ./...
	CGO_ENABLED=0 GOOS=windows $(GO) vet ./internal/archive ./internal/lock ./internal/delegate
	CGO_ENABLED=0 GOARCH=arm $(GO) vet ./internal/... ./skills/...
	CGO_ENABLED=0 $(GO) build -o /dev/null ./cmd/wacli 2>&1 | grep -q wacli_requires_cgo_enabled_1_for_go_sqlite3

## lint: formatting, vet, unreachable code, known vulnerabilities
lint:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	$(GO) vet ./...
	@out=$$($(GO) run $(DEADCODE) ./... && $(GO) run $(DEADCODE) -test ./...); \
		if [ -n "$$out" ]; then echo "$$out"; exit 1; fi
	$(GO) run $(GOVULNCHECK) ./...

## check: everything CI runs
check: lint test build
