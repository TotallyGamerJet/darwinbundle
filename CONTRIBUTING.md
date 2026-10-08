# Contributing

Thanks for looking. This is a small library with a few firm rules; they are
short, and each has a reason.

## Setup

You need Go 1.26 or newer. On a Mac you can run everything; elsewhere the tests
that need Apple's tools or the Keychain skip themselves, and CI runs those.

```sh
go test ./...
golangci-lint run ./...      # v2; see .golangci.yml
```

### The Keychain tests

Tests that need a code-signing identity are **opt-in**, because the way to give
them one is to put a throwaway keychain on your user keychain search list for the
length of the run:

```sh
MACBUNDLE_TEST_KEYCHAIN=1 go test ./...
```

It creates a keychain holding generated identities, restores your search list
when it finishes, and holds a lock so that packages running in parallel do not
trample each other's changes to it. Nothing touches your login keychain and
nothing prompts. If a run is killed outright it may leave an entry on the list
pointing at a deleted file; the next run removes it. See
`internal/keychaintest`.

For the `keychain` package's own tests you can instead set `SIGNING_IDENTITY` to
the name of an identity you already have. That will raise Keychain prompts.

## The rules

**`CGO_ENABLED=0` everywhere.** Native calls go through purego, and only
`keychain` and `internal/cocoa` import it. A cgo dependency would end
cross-compilation, which is the point of the module.

**Never discard an error.** `_ = f.Close()` is not handling an error, it is
hiding one behind a character that looks deliberate. In order of preference:

1. Join it into the result: name the return and
   `defer func() { err = errors.Join(err, f.Close()) }()`. This is the default
   for anything that writes, since a write is not durable until `Close` says so.
2. Log it, when there is no result to join it to.
3. In a test, report it through `t`.

`errcheck` runs with `check-blank: true`, and there are no exclusion presets
(`std-error-handling` would exempt every `Close`). There are no `//nolint`
directives in the tree, and a new one needs a good reason.

**Say why, not what.** The comments that earn their place here record something
that cost time to find out: the format's silent failure modes, a platform quirk,
the reason a default is what it is. See `docs/asset-catalog-format.md`.

**Check against the authority.** A test that compares this module's output with
its own reader proves only that it agrees with itself. Where Apple's tool exists
(`assetutil`, `actool`, `lsbom`, `plutil`, `codesign`) the test should ask it.

**Test the failure you are fixing.** A fix without a test that fails first is a
guess. If you can, write the test, watch it fail, then fix.

## Commits

Small and focused, so that each is easy to follow: one change, with its tests,
that builds and passes on its own. The message says what changed and, more
importantly, *why*. A commit that mixes a behaviour change with a reformat, or a
fix with an unrelated cleanup, will be asked to split.

## Adding a platform-specific file

Files for one platform go behind a build tag with a counterpart for the others,
so that the package always compiles:

```
keychain_darwin.go   //go:build darwin
keychain_other.go    //go:build !darwin
```

CI cross-builds for Linux, macOS and Windows and vets the tests too, because a
platform file that fails to compile elsewhere breaks everyone importing the
package there. Lint it for both: `GOOS=linux golangci-lint run ./...`.

## Fuzzing

The parsers read untrusted input and the property encoder handles caller text.
Their seed corpora run as ordinary tests. To fuzz for real:

```sh
go test ./bom -run '^$' -fuzz '^FuzzParse$' -fuzztime 1m
```

CI does this weekly. If the fuzzer finds something, the failing input lands in
`testdata/fuzz/`; commit it with the fix.

## Releasing

Releases are tags. Tag `vX.Y.Z` on `main` once CI is green, then:

```sh
gh release create vX.Y.Z --generate-notes
```

While anchore/quill#883 is unmerged, the `replace` in `go.mod` is not inherited
by users, so say so in the notes if the release touches signing.
