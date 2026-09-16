GO ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test vet lint generate smoke tidy tidy-check vuln docs

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/hamn ./cmd/hamn

test:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

lint:
	golangci-lint run ./...

generate:
	$(GO) generate ./...

smoke:
	$(GO) test -tags live -count=1 ./test/live/...

tidy:
	$(GO) mod tidy

# Fails when go.mod/go.sum are not tidy.
tidy-check:
	$(GO) mod tidy -diff

vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

# Regenerates docs/TOOLS.md and docs/tools.json and fails if they changed.
docs:
	$(GO) run ./internal/tools/docsgen
	git diff --exit-code docs
