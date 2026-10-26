# Contributing

Hamn is small on purpose. Open a short issue before a big PR.

## Setup

Go 1.25+, plus golangci-lint v2 for linting.

```sh
make build      # bin/hamn
make test       # go test -race ./...
make vet
make lint
make docs       # regenerates docs/TOOLS.md and docs/tools.json, fails on any diff
make spec       # spec index only; needs HAMN_SPEC=/path/to/openapi.oas.json
```

CI fails if docsgen changes anything, so run `make docs` and commit what it writes.

## No live writes

Tests never write to a real App Store Connect account. Everything runs against fakes. `make smoke` uses your real key, but read-only, and the transport refuses any non-GET. Keep it boring.

## Fixtures and goldens

Hand-write fixtures from the spec's examples, or record them:

```sh
go run -tags dev ./cmd/hamn-record -app 1234567890 -out testdata/fixtures
```

The recorder is GET-only and swaps out ids, names and emails. Read what it wrote anyway. `go test ./... -update` rewrites goldens. Review the diff, because goldens are the contract. A redaction scan fails if anything secret-looking lands in one.

## Adding a tool

New tools go in a non-default toolset unless agreed in an issue; the default list has a fixed byte budget.

1. Register it in `internal/tools/<toolset>` with `tool.New` in `init()`.
2. Pick the class honestly: read, write or destructive. The name's verb must match or the registry test fails. Public or irreversible means destructive.
3. Gate it in `tool.Meta`. `Confirm: tool.ConfirmByPlan` (embed `tool.ConfirmPlan`) when the change depends on remote state and must be re-checked; `Confirm: tool.ConfirmByFlag` (embed `tool.ConfirmFlag`) for a preview followed by `confirm:true`. Add `RequireHuman: true` for public, people, money or irreversible actions; the client then asks the human, and calls without elicitation are refused. Destructive tools need a Confirm mode.
4. Description ≤ 280 characters. Enums go in the schema.
5. Accept names as well as ids through `env.Resolve` (`app`, `version`, `build`, `group`, locales).
6. Wrap third-party text as untrusted. The client does it for spec free-text fields; check anything you build by hand.
7. Removal only through an explicit `remove` list.
8. Fan out with `tool.Parallel`. If you wait on Apple and `env.HasProgress()` is false, cap the wait at `tool.NoProgressWaitCap` and return a resume step.
9. Test via `tooltest`, including the preview and stale-plan paths. CI runs on Windows too, so no POSIX assumptions.
10. `make docs` and make sure the context budget test still passes.

Tool names, arguments and output fields are a public API that agents depend on. Add optional arguments instead of renaming. If you must rename, say so in the PR and `CHANGELOG.md`.

## PRs

[Conventional Commits](https://www.conventionalcommits.org/) (`feat(tools): ...`, `fix(asc): ...`), since the changelog is built from them. Say what you tested.

Contributions are MIT licensed and covered by the [Code of Conduct](CODE_OF_CONDUCT.md).
