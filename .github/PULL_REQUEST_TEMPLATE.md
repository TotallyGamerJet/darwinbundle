## What and why

<!-- What changes, and — more to the point — why. -->

## Checklist

- [ ] One focused change, with its tests, that builds and passes on its own
- [ ] A test that failed before the fix, for a fix
- [ ] `go test ./...` and `golangci-lint run ./...` pass (and `GOOS=linux golangci-lint run ./...` if a platform file changed)
- [ ] No `_ =` on an error and no new `//nolint`
- [ ] Output checked against Apple's own tool where one exists
