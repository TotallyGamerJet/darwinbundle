package darwinbundle

import (
	"crypto"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/anchore/quill/quill"
	"github.com/anchore/quill/quill/pki"
	"github.com/anchore/quill/quill/pki/load"
)

// Signer is a code-signing identity: a key that signs digests, and the
// certificate chain that vouches for it.
//
// Package keychain provides one backed by the macOS Keychain, whose key never
// leaves the Security framework. Anything else that can sign — a hardware token,
// a cloud KMS — can be adapted by implementing these two methods.
type Signer interface {
	crypto.Signer

	// Certificates returns the signing certificate followed by its issuers: the
	// leaf first, then intermediates, root last if present. This is the order
	// the Security framework and most tooling produce.
	Certificates() []*x509.Certificate
}

// SignConfig says how to sign. Exactly one of Signer, P12Path and AdHoc must be
// set.
//
// Nothing is defaulted. A configuration that names no identity is an error
// rather than an ad hoc signature, because the two look identical until the
// result fails to load: an ad hoc signature belongs to no team, so the sandbox
// refuses it a team-prefixed App Group and an extension signed that way cannot
// read its own configuration. Choosing ad hoc has to be something you wrote.
type SignConfig struct {
	// Signer signs with a certificate and key held somewhere that never gives
	// up the key, such as the Keychain. It is the preferred way: exporting an
	// identity to a PKCS#12 file puts a private key on disk, usually somewhere
	// that later gets archived, synced or committed by accident.
	Signer Signer

	// P12Path and P12Password sign with a certificate and key from a PKCS#12
	// file. This is for environments with no Keychain, a CI runner holding the
	// identity as a secret above all.
	P12Path     string
	P12Password string

	// AdHoc signs without a certificate. That is enough to check that a bundle's
	// structure and resource seal are right, and it is what Apple Silicon wants
	// of any code it runs at all, but macOS will not load an extension that
	// needs an App Group from code signed this way.
	AdHoc bool

	// Identifier is the identifier recorded in the code directory, what
	// codesign's -i sets. Empty means the bundle's own identifier, which is what
	// codesign does.
	Identifier string

	// Entitlements is the path of an entitlements plist applied to what is being
	// signed. Each binary or bundle takes its own: the host application and its
	// extension generally have different ones.
	Entitlements string

	// TimestampServer is the URL of an RFC 3161 timestamp service. A trusted
	// timestamp is what lets a signature outlive the certificate's expiry, and
	// notarisation requires one. It needs a certificate, so it cannot be
	// combined with AdHoc.
	TimestampServer string
}

// Sign signs the Mach-O binary, universal binary or bundle at path with quill.
//
// Signing is pure Go and runs anywhere. Checking the result is not: only Apple's
// codesign can say whether macOS will accept a signature, so Verify needs a Mac.
//
// Sign a bundle's contents from the inside out. Signing an application seals
// the signatures of the extensions nested in it by reference, so an extension
// signed after the application invalidates the application's seal.
func Sign(path string, cfg SignConfig) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("darwinbundle: nothing to sign at %s: %w", path, err)
	}
	if cfg.Entitlements != "" {
		if _, err := os.Stat(cfg.Entitlements); err != nil {
			return fmt.Errorf("darwinbundle: entitlements file %s: %w", cfg.Entitlements, err)
		}
	}

	sc, err := cfg.quillConfig(path)
	if err != nil {
		return err
	}
	if cfg.Entitlements != "" {
		sc = sc.WithEntitlements(cfg.Entitlements)
	}
	if cfg.Identifier != "" {
		sc = sc.WithIdentity(cfg.Identifier)
	}
	if cfg.TimestampServer != "" {
		sc = sc.WithTimestampServer(cfg.TimestampServer)
	}

	if err := quill.Sign(*sc); err != nil {
		if isNestedBundleRefusal(err) {
			return fmt.Errorf("darwinbundle: signing %s: %w: %w", path, ErrNestedBundlesUnsupported, err)
		}
		return fmt.Errorf("darwinbundle: signing %s: %w", path, err)
	}
	return nil
}

// ErrNestedBundlesUnsupported is returned, wrapped, when the build of quill in
// use declines to sign an application because it contains another bundle, an
// app extension for instance. Test for it with errors.Is.
//
// It is a limitation of quill rather than of this package, and it is temporary:
// anchore/quill#883 teaches quill to seal nested bundles by reference, the way
// codesign does. Until a release contains it, a program that signs such an
// application has to build against a quill that does, by adding to its go.mod
//
//	replace github.com/anchore/quill => github.com/TotallyGamerJet/quill <version>
//
// with the version this module's own go.mod names. A replace in a dependency's
// go.mod is not applied to the programs that import it, which is why this
// cannot be done for you. The README has the current details.
//
// Signing an application that contains no other bundle is not affected.
var ErrNestedBundlesUnsupported = errors.New(
	"this quill cannot seal an application that contains another bundle (anchore/quill#883); see ErrNestedBundlesUnsupported")

// nestedBundleRefusal is how quill words that refusal. It is matched as text
// because quill reports it as a plain error, so a wording change upstream makes
// the hint disappear and leaves quill's own message, which is still accurate.
const nestedBundleRefusal = "signing nested bundles is not supported"

func isNestedBundleRefusal(err error) bool {
	return strings.Contains(err.Error(), nestedBundleRefusal)
}

func (c SignConfig) validate() error {
	var chosen []string
	if c.Signer != nil {
		chosen = append(chosen, "Signer")
	}
	if c.P12Path != "" {
		chosen = append(chosen, "P12Path")
	}
	if c.AdHoc {
		chosen = append(chosen, "AdHoc")
	}
	switch len(chosen) {
	case 0:
		return errors.New("darwinbundle: SignConfig names no signing identity; set Signer, P12Path or AdHoc " +
			"(AdHoc is a real choice, but it has to be made)")
	case 1:
	default:
		return fmt.Errorf("darwinbundle: SignConfig sets %v; choose exactly one of Signer, P12Path and AdHoc", chosen)
	}
	if c.AdHoc && c.TimestampServer != "" {
		return errors.New("darwinbundle: a timestamp needs a certificate, so TimestampServer cannot be used with AdHoc")
	}
	return nil
}

// quillConfig turns the chosen identity source into quill's configuration.
func (c SignConfig) quillConfig(path string) (*quill.SigningConfig, error) {
	switch {
	case c.Signer != nil:
		certs := c.Signer.Certificates()
		if len(certs) == 0 {
			return nil, errors.New("darwinbundle: the Signer reports no certificates")
		}
		// An ad hoc configuration is the base so the fields that are not about
		// signing material are set the way quill expects; the material is then
		// supplied.
		sc, err := quill.NewSigningConfigFromPEMs(path, "", "", "", false)
		if err != nil {
			return nil, fmt.Errorf("darwinbundle: preparing the signature: %w", err)
		}
		sc.SigningMaterial = pki.SigningMaterial{
			Signer:          c.Signer,
			Certs:           quillChainOrder(certs),
			TimestampServer: c.TimestampServer,
		}
		return sc, nil

	case c.P12Path != "":
		contents, err := load.P12(c.P12Path, c.P12Password)
		if err != nil {
			return nil, fmt.Errorf("darwinbundle: reading the signing certificate %s: %w", c.P12Path, err)
		}
		// failWithoutFullChain is false so that a certificate without a bundled
		// intermediate can still be used; the chain is checked at verification.
		sc, err := quill.NewSigningConfigFromP12(path, *contents, false)
		if err != nil {
			return nil, fmt.Errorf("darwinbundle: preparing the signature: %w", err)
		}
		return sc, nil

	default: // AdHoc; validate has ruled out anything else
		// An empty certificate makes quill produce an ad hoc signature.
		sc, err := quill.NewSigningConfigFromPEMs(path, "", "", "", false)
		if err != nil {
			return nil, fmt.Errorf("darwinbundle: preparing an ad hoc signature: %w", err)
		}
		return sc, nil
	}
}

// quillChainOrder reverses a leaf-first chain into the order quill expects.
//
// quill's SigningMaterial.Leaf takes the last certificate and returns nil if it
// is a CA, so it wants the chain with the leaf last. The Security framework and
// most tooling produce the conventional opposite, leaf first, and that is the
// order Signer.Certificates promises.
//
// Passing the wrong way round is not a loud failure. quill finds a CA where it
// expects the leaf, returns nil, and silently omits the Team ID from the code
// directory — a signature that verifies perfectly and is then refused access to
// a team-prefixed App Group, with nothing anywhere saying why.
func quillChainOrder(chain []*x509.Certificate) []*x509.Certificate {
	out := make([]*x509.Certificate, len(chain))
	for i, c := range chain {
		out[len(chain)-1-i] = c
	}
	return out
}
