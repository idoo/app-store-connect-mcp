GO ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test vet lint spec smoke tidy tidy-check vuln docs snapshot mcpb

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/app-store-connect-mcp ./cmd/app-store-connect-mcp

test:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

lint:
	golangci-lint run ./...

# Regenerates the embedded spec index; gen fails without ASC_MCP_SPEC (path to
# Apple's openapi.oas.json).
spec:
	$(GO) generate ./internal/spec

smoke:
	$(GO) test -tags live -count=1 ./test/live/...

tidy:
	$(GO) mod tidy

# Fails when go.mod/go.sum are not tidy.
tidy-check:
	$(GO) mod tidy -diff

vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

# Local release dry run (no publish, no signing, no image): dist/ + .mcpb.
snapshot:
	goreleaser release --snapshot --clean --skip=publish,sign,docker

mcpb: snapshot
	scripts/build-mcpb.sh

# Regenerates docs/TOOLS.md and docs/tools.json and fails if they changed.
docs:
	$(GO) run ./internal/tools/docsgen
	git diff --exit-code docs
