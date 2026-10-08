// Package macbundle builds, signs and packages macOS application bundles from Go,
// without Xcode and, for most of it, without a Mac.
//
// The pieces are the ones a release build of an app needs, each usable on its
// own:
//
//   - Bundle assembles Name.app, with app extensions nested in it, around
//     binaries you have already compiled: the directory layout, Info.plist,
//     the executable's permissions, resources, and an icon compiled from an
//     .appiconset.
//   - Sign signs a bundle or a binary with a certificate from a PKCS#12 file, a
//     Keychain identity (package keychain), or ad hoc.
//   - Verify and Inspect ask Apple's codesign what it makes of the result.
//   - MakeUniversal merges per-architecture binaries into one universal binary.
//   - Zip archives a bundle, keeping the symbolic links and executable bits that
//     a bundle needs to survive the trip.
//
// Because none of this shells out to Apple's tools to produce its output, a
// distribution build can run on a Linux CI runner. The exception is checking a
// signature: only codesign can say whether macOS will accept one, so Verify and
// Inspect need a Mac, and say so plainly elsewhere.
//
// # Packages
//
// This package is the one most programs need. The rest are what it is built
// from, exported because they are useful alone:
//
//   - appiconset compiles an .appiconset directory into an Assets.car.
//   - assetcatalog writes the CoreUI structures inside an Assets.car.
//   - bom reads and writes Apple's Bill of Materials container.
//   - keychain signs with an identity held in the macOS Keychain, so the
//     private key never has to be exported (macOS only).
//
// # Requirements
//
// Go 1.26 or newer, and nothing else: no cgo, no Xcode. Building, signing with
// a certificate file or ad hoc, universal binaries and archives all work on any
// platform Go does. Only Verify, Inspect and package keychain need a Mac.
//
// One limitation is worth knowing before you start. Signing an application that
// contains an app extension needs a fix to quill that is not yet in a release
// (anchore/quill#883). Without it, Sign returns ErrNestedBundlesUnsupported,
// which explains what to add to your go.mod. Everything else is unaffected.
//
// # An example
//
// See the Example in this package for assembling, signing and archiving an
// application with an extension.
package macbundle
