## What and why

<!-- One or two sentences; link the issue. Conventional-commit title (feat/fix/docs/...; `!` for breaking). -->

## Checklist

- [ ] `go test -race ./...` and `golangci-lint run` pass
- [ ] New or changed tools: fixtures under `testdata/fixtures/`, goldens regenerated with `-update` and reviewed
- [ ] `go generate ./...` run; `docs/TOOLS.md` and `docs/tools.json` committed
- [ ] No breaking wire-API change (removed tool/argument, newly required argument, R → W/D), or it follows the deprecation policy
- [ ] Write/destructive paths go through plan/confirm; nothing deletes by omission
- [ ] No live App Store Connect writes were used to test this; no keys, JWTs or real ids in fixtures
