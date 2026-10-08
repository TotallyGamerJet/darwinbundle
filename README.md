# macbundle

[![CI](https://github.com/TotallyGamerJet/macbundle/actions/workflows/ci.yml/badge.svg)](https://github.com/TotallyGamerJet/macbundle/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/TotallyGamerJet/macbundle.svg)](https://pkg.go.dev/github.com/TotallyGamerJet/macbundle)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

Build, sign and package macOS application bundles from Go — without Xcode, and
for most of it without a Mac.

Compile your binaries with `GOOS=darwin`, then let `macbundle` do the rest:
assemble `Name.app` with app extensions nested inside it, compile an icon,
merge architectures into a universal binary, code-sign everything, and zip it
with the symlinks and executable bits a bundle needs to survive the trip.
Building, signing and packaging all run on a Linux CI runner; only *checking* a
signature needs a Mac.

```go
// Errors elided for brevity.
app := &macbundle.Bundle{
	Kind:       macbundle.KindApp,
	Name:       "Notes",
	Identifier: "com.example.Notes",
	Version:    "1.4.0",
	Executable: "build/notes",
	IconSet:    "assets/AppIcon.appiconset",
}
path, err := app.Build("dist") // dist/Notes.app

err = macbundle.Sign(path, macbundle.SignConfig{
	P12Path:     "developer-id.p12",
	P12Password: os.Getenv("P12_PASSWORD"),
})

err = macbundle.Zip("dist/Notes.zip", path)
```

There is a complete release build — universal binaries, an extension, signing
from the inside out, verification — in the package
[examples](https://pkg.go.dev/github.com/TotallyGamerJet/macbundle#example-package).

## Install

```sh
go get github.com/TotallyGamerJet/macbundle
```

Go 1.26 or newer. No cgo, and nothing to install.

## What is in it

`macbundle` is the one package most programs need. The others are what it is
built from, and each is useful on its own.

| Package | What it does | Where it runs |
|---|---|---|
| [`macbundle`](https://pkg.go.dev/github.com/TotallyGamerJet/macbundle) | `Bundle` (assemble), `Sign`, `Verify`, `Inspect`, `MakeUniversal`, `Zip` | Everywhere, except `Verify` and `Inspect`, which need `codesign` |
| [`appiconset`](https://pkg.go.dev/github.com/TotallyGamerJet/macbundle/appiconset) | Compile an `.appiconset` directory into an `Assets.car` | Everywhere |
| [`assetcatalog`](https://pkg.go.dev/github.com/TotallyGamerJet/macbundle/assetcatalog) | Write the CoreUI structures inside an `Assets.car` | Everywhere |
| [`bom`](https://pkg.go.dev/github.com/TotallyGamerJet/macbundle/bom) | Read and write Apple's Bill of Materials container (`.pkg` receipts, `.car` files) | Everywhere |
| [`keychain`](https://pkg.go.dev/github.com/TotallyGamerJet/macbundle/keychain) | A `crypto.Signer` backed by an identity in the macOS Keychain | macOS (compiles everywhere) |

## Signing

`Sign` takes exactly one identity source, and refuses to guess:

| `SignConfig` field | Use it for |
|---|---|
| `Signer` | An identity whose key never leaves where it lives. `keychain.Find` gives you one for the macOS Keychain. **Preferred**: nothing exports a private key to disk. |
| `P12Path` / `P12Password` | A CI runner holding the identity as a secret. |
| `AdHoc` | Checking structure, or code that needs no team. |

A `SignConfig` that names none of them is an **error**, not an ad hoc signature.
The two look identical until the result fails to load: ad hoc code belongs to no
team, so the sandbox refuses it a team-prefixed App Group, and an extension
signed that way can't read its own configuration. Choosing ad hoc has to be
something you wrote down.

Sign a bundle **from the inside out**: the application's signature seals the
extensions nested in it, so sign each extension first, then the application.

```go
signer, err := keychain.Find("Developer ID Application")
defer signer.Close()

for _, p := range []string{extensionPath, appPath} {
	err := macbundle.Sign(p, macbundle.SignConfig{Signer: signer})
}
```

Signing is portable, but **checking a signature is not**: only Apple's
`codesign` can say whether macOS will accept one. `Verify` and `Inspect` run it
and return `ErrCodesignUnavailable` on a machine without it.

### Applications that contain extensions

> **This needs one line in your `go.mod` until [anchore/quill#883][quill-883]
> is released.**

Signing an application that contains an app extension depends on a fix to
[quill], the library that does the signing. It is not in a release yet. Until it
is, `Sign` returns `ErrNestedBundlesUnsupported` for such an application, with
quill's own explanation. To use the fix, add this to **your** `go.mod`:

```
replace github.com/anchore/quill => github.com/TotallyGamerJet/quill v0.0.0-20260930133131-f12170528b7d
```

A `replace` in a library's `go.mod` is ignored by the programs that import it,
which is why this can't be done for you. This module's own `go.mod` carries the
same line, so its tests exercise the real thing, and CI also runs them with the
line removed to check that what you get without it is a recognisable error and
not something worse. Everything that doesn't involve nesting works against
upstream quill as it is.

(Separately: quill's latest *release*, v0.7.1, predates its support for signing
application bundles at all, so this module requires a commit from its `main`
branch. That one is inherited automatically; nothing to do.)

[quill]: https://github.com/anchore/quill
[quill-883]: https://github.com/anchore/quill/pull/883

## Describing a bundle

`Bundle` generates the Info.plist keys every bundle has from its fields, and
takes everything else from `Info`, which wins where the two overlap. That is how
anything specific to a kind of bundle stays out of the library:

```go
&macbundle.Bundle{
	Kind:        macbundle.KindExtension,
	Name:        "NotesSync",
	DisplayName: "Notes",                 // what the user sees
	Identifier:  "com.example.Notes.Sync", // must be prefixed by the app's
	Executable:  "build/sync",
	Info: map[string]any{
		"NSExtension": map[string]any{
			"NSExtensionPointIdentifier": "com.apple.fileprovider-nonui",
			"NSExtensionPrincipalClass":  "SyncExtension",
		},
	},
}
```

Everything is checked before anything is written: names usable as a path
element, identifiers in the character set Apple documents, an extension's
identifier prefixed by its application's, resource paths that stay inside
`Contents/Resources`, and Info values a property list can actually hold. A build
assembles in a temporary directory and replaces the destination only on success,
so a failed rebuild never destroys the last good bundle.

## Scope

**It does:** assemble, sign, verify, merge architectures, compile app icons,
archive.

**It doesn't:** notarise, build `.dmg` or `.pkg` installers, handle frameworks,
or produce Mac App Store packages. Compiling your Go code for `darwin` is also
yours; `macbundle` starts from the binaries.

## Why pure Go

Everything here is normally done by Apple's tools — `actool` (which needs Xcode
installed), `lipo`, `ditto` — which exist only on a Mac. That makes a release
build a job that can only run on a Mac runner. Here a release build needs a Go
toolchain and nothing else, so the same job runs wherever the rest of your CI
does, and `CGO_ENABLED=0` cross-compiles cleanly.

The Apple tools are still used where they are the authority: on macOS the tests
check this module's output against `actool`, `assetutil`, `lsbom`, `plutil` and
`codesign`, so "Apple's reader accepts it" is tested, not assumed. See
[docs/asset-catalog-format.md](docs/asset-catalog-format.md) for what was
learned getting `Assets.car` right — most of the format is undocumented, and the
ways it fails are silent.

Output is reproducible for a given Go toolchain. Icon pixels are compressed with
the standard library's gzip, whose output may differ between Go releases, so two
toolchains can write different bytes that decode to the same pixels.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). To report a security problem, see
[SECURITY.md](SECURITY.md).

## Provenance

Extracted from sftp-extension, a macOS File Provider extension written entirely
in Go, where it was built to ship that extension. Signing is by [anchore/quill][quill], universal binaries by
[konoui/lipo](https://github.com/konoui/lipo), and Keychain access by
[purego](https://github.com/ebitengine/purego).

## License

[Apache-2.0](LICENSE).
