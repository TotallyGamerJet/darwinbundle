// Package keychain signs with a code-signing identity held in the macOS
// Keychain, so the private key never has to leave it.
//
// The usual alternative is to export the identity to a PKCS#12 file so that a
// tool can read the key. That puts a developer's private key on disk, in a
// directory that later gets archived, synced or committed by accident. Here the
// key stays where the user put it: signing is done by the Security framework
// through SecKeyCreateSignature, which takes a digest and returns a signature.
//
// A Signer satisfies macbundle.Signer, so it can be given straight to
// macbundle.Sign:
//
//	signer, err := keychain.Find("Developer ID Application")
//	if err != nil { ... }
//	defer signer.Close()
//	err = macbundle.Sign(path, macbundle.SignConfig{Signer: signer})
//
// This package works only on macOS. Elsewhere it compiles, and Find and
// Identities return ErrUnsupported, so a program can import it unconditionally.
//
// # Keychain prompts
//
// The first signature with a key that does not yet trust the calling program
// raises a Keychain authorisation prompt. Find performs one throwaway signature
// so that this happens once, at a moment the caller can expect, rather than in
// the middle of signing. Signing a bundle takes several signatures — quill signs
// each Mach-O twice, once to size the signature and once for real — and when the
// program is not yet on the key's access list they are all issued at once, so
// the user would otherwise face a stack of identical dialogs of which "Always
// Allow" dismisses only the one it was clicked on.
package keychain

import "errors"

// ErrUnsupported is returned by Find and Identities on platforms other than
// macOS, where there is no Keychain to search.
var ErrUnsupported = errors.New("keychain: the macOS Keychain is only available on macOS; " +
	"to sign elsewhere use a PKCS#12 file (macbundle.SignConfig.P12Path)")

// Identity describes one code-signing identity in the Keychain.
//
// It carries no reference to the identity or its key, only what is needed to
// choose between them.
type Identity struct {
	// Name is the certificate's common name, which is what
	// `security find-identity` prints and what Find matches against.
	Name string

	// TeamID is the certificate's organizational unit. It is the value that has
	// to match an App Group's prefix: the sandbox grants a team-prefixed group
	// only to code signed by that team, so an identity belonging to any other
	// team produces a build as unusable as an ad hoc one. It is empty for
	// certificates that carry none, Apple's own platform certificates among them.
	TeamID string
}
