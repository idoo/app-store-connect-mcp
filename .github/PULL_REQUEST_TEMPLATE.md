## What and why

<!-- A sentence or two, plus the issue link. Conventional-commit title (`!` for breaking). -->

## Checklist

- [ ] `go test -race ./...` and `golangci-lint run` pass
- [ ] Tool changes: fixtures in `testdata/fixtures/`, goldens updated with `-update` and actually read
- [ ] `make docs` run, `docs/TOOLS.md` and `docs/tools.json` committed
- [ ] No breaking change to tool names, arguments or classes, or it's called out
- [ ] Writes go through plan/confirm; nothing deletes by omission
- [ ] No live App Store Connect writes in testing; no keys, JWTs or real ids in fixtures
