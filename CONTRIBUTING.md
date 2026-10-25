# Contributing

Thanks for helping. Hamn is small on purpose, so open a short issue before a big PR.

## Setup

You need Go 1.25+ and, for linting, golangci-lint v2.

```sh
git clone https://github.com/idoo/hamn && cd hamn
make build      # bin/hamn
make test       # go test -race ./...
make vet
make lint
make generate   # regenerates docs/TOOLS.md and docs/tools.json (the spec index needs HAMN_SPEC)
```

CI fails if `go generate` changes anything, so commit regenerated files with your change.

## The one hard rule: no live writes

Tests never write to a real App Store Connect account. Unit, tool and protocol tests run against fakes. The live smoke test (`make smoke`, build tag `live`) uses your real key, forces read-only and refuses every non-GET at the HTTP transport. Keep it that way.

## Fixtures and goldens

- Fixtures live next to the tests in `testdata/`. Hand-write them from the spec's examples, or record real responses:

  ```sh
  go run -tags dev ./cmd/hamn-record -app 1234567890 -out testdata/fixtures
  ```

  The recorder is GET-only, checks every request against the spec, and runs responses through a sanitizer that replaces ids, names and emails by default. Read what it wrote before you commit it.
- Golden outputs are rewritten with `go test ./... -update`. Review the diff; goldens are the contract.
- A redaction scan runs over the goldens. If it fails, something secret-looking got into an output.

## Adding a tool

1. Pick the toolset package in `internal/tools/<toolset>` and register the tool in its `init()` with `tool.New`.
2. Choose the class honestly: read, write or destructive. The name's verb must match the class, or the registry test fails. Public or irreversible actions are destructive.
3. Pick the confirm mode: `plan` when the change depends on remote state, `human` for public, people or irreversible actions.
4. Keep the description at most 280 characters. Put enums in the schema, not the prose.
5. Accept names as well as ids through `env.Resolve` where it makes sense (`app`, `version`, `build`, `group`, locales).
6. Wrap third-party text as untrusted (the client does it for spec free-text attributes; check anything you build by hand).
7. Never remove things by omission. Removal goes through an explicit `remove` list.
8. Fan out API calls with `tool.Parallel` (bounded, cancels on first error). A tool that waits on Apple must cap the wait at `tool.NoProgressWaitCap` when `env.HasProgress()` is false, and return a resume step.
9. Test through `tooltest` against the spec-checking fake, including the preview and stale-plan paths for mutations. CI also runs the tests on Windows, so don't assume POSIX paths or file modes in tests.
10. Run `make generate` to refresh `docs/TOOLS.md` and `docs/tools.json`, and check the context budget test still passes.

## Compatibility

Tool names, argument names and output fields are a public API that agents and skills depend on. Prefer adding optional arguments over renaming. If a rename is unavoidable, call it out in the PR and in `CHANGELOG.md`.

## Commits and PRs

Use [Conventional Commits](https://www.conventionalcommits.org/) (`feat(tools): ...`, `fix(asc): ...`, `docs: ...`); the release changelog is grouped from them. Keep PRs focused, and say in the description what you tested and how.

By contributing you agree that your contribution is licensed under the MIT License, and you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md).
