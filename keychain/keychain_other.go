//go:build !darwin

package keychain

import (
	"crypto"
	"crypto/x509"
	"io"
)

// Signer signs with a Keychain identity. It cannot be created off macOS; the
// type exists so that code mentioning it compiles everywhere.
type Signer struct{}

// Find reports ErrUnsupported: there is no Keychain off macOS.
func Find(string) (*Signer, error) { return nil, ErrUnsupported }

// Identities reports ErrUnsupported: there is no Keychain off macOS.
func Identities() ([]Identity, error) { return nil, ErrUnsupported }

// Public returns nil: there is no key off macOS.
func (*Signer) Public() crypto.PublicKey { return nil }

// Certificates returns nil: there is no identity off macOS.
func (*Signer) Certificates() []*x509.Certificate { return nil }

// Certificate returns nil: there is no identity off macOS.
func (*Signer) Certificate() *x509.Certificate { return nil }

// Sign reports ErrUnsupported: there is no key off macOS.
func (*Signer) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return nil, ErrUnsupported
}

// Close does nothing: there is nothing to release.
func (*Signer) Close() error { return nil }
