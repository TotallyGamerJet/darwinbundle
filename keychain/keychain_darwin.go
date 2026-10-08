//go:build darwin

package keychain

import (
	"crypto"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/cstrings"
	"github.com/ebitengine/purego/objc"

	"github.com/TotallyGamerJet/macbundle/internal/cocoa"
)

// Signer signs with a private key held in the Keychain.
//
// It satisfies crypto.Signer, which is what quill's signing material accepts,
// and macbundle.Signer. A Signer holds a reference to the key until Close is
// called.
type Signer struct {
	// cert is the leaf certificate. Its public key is the signer's public key,
	// so no separate lookup is needed for that.
	cert *x509.Certificate

	// chain is the leaf plus whatever intermediates the system could resolve.
	chain []*x509.Certificate

	// mu orders Close against Sign: a Sign in flight holds the key, and Close
	// must not release it from under one.
	mu sync.RWMutex

	// key is a SecKeyRef, retained until Close.
	key objc.ID
}

// Security framework functions, bound on first use.
var (
	securityOnce sync.Once
	securityErr  error

	secItemCopyMatching       func(query objc.ID, result *objc.ID) int32
	secIdentityCopyCert       func(identity objc.ID, out *objc.ID) int32
	secIdentityCopyPrivateKey func(identity objc.ID, out *objc.ID) int32
	secCertificateCopyData    func(cert objc.ID) objc.ID
	secKeyCreateSignature     func(key objc.ID, algorithm objc.ID, data objc.ID, errOut *objc.ID) objc.ID
	secTrustCreateWithCerts   func(certs objc.ID, policies objc.ID, out *objc.ID) int32
	secPolicyCreateBasicX509  func() objc.ID
	secTrustEvaluateWithError func(trust objc.ID, errOut *objc.ID) bool
	secTrustCopyCertChain     func(trust objc.ID) objc.ID
)

func loadSecurity() error {
	securityOnce.Do(func() {
		if err := cocoa.Load(); err != nil {
			securityErr = err
			return
		}
		defer func() {
			if r := recover(); r != nil {
				securityErr = fmt.Errorf("keychain: binding Security functions: %v", r)
			}
		}()
		purego.RegisterLibFunc(&secItemCopyMatching, purego.RTLD_DEFAULT, "SecItemCopyMatching")
		purego.RegisterLibFunc(&secIdentityCopyCert, purego.RTLD_DEFAULT, "SecIdentityCopyCertificate")
		purego.RegisterLibFunc(&secIdentityCopyPrivateKey, purego.RTLD_DEFAULT, "SecIdentityCopyPrivateKey")
		purego.RegisterLibFunc(&secCertificateCopyData, purego.RTLD_DEFAULT, "SecCertificateCopyData")
		purego.RegisterLibFunc(&secKeyCreateSignature, purego.RTLD_DEFAULT, "SecKeyCreateSignature")
		purego.RegisterLibFunc(&secTrustCreateWithCerts, purego.RTLD_DEFAULT, "SecTrustCreateWithCertificates")
		purego.RegisterLibFunc(&secPolicyCreateBasicX509, purego.RTLD_DEFAULT, "SecPolicyCreateBasicX509")
		purego.RegisterLibFunc(&secTrustEvaluateWithError, purego.RTLD_DEFAULT, "SecTrustEvaluateWithError")
		purego.RegisterLibFunc(&secTrustCopyCertChain, purego.RTLD_DEFAULT, "SecTrustCopyCertificateChain")
	})
	return securityErr
}

// OSStatus values acted on.
const (
	errSecSuccess      = 0
	errSecItemNotFound = -25300
)

// SecItem dictionary keys and values.
//
// These are the string contents of the framework's exported CFStringRef
// constants: kSecClass is the CFString "class", kSecClassIdentity is "idnt", and
// so on. They are used as literals rather than read from the exported globals
// because reading a global means converting a dlsym address into a pointer, and
// CFDictionary compares keys with CFEqual, which for strings compares content.
// An NSString with the same content is therefore the same key, and NSString is
// toll-free bridged to CFStringRef.
//
// A wrong constant fails by finding nothing, so the tests exercise the real
// Keychain rather than trusting these.
const (
	kSecClass         = "class"
	kSecClassIdentity = "idnt"
	kSecReturnRef     = "r_Ref"
	kSecMatchLimit    = "m_Limit"
	kSecMatchLimitAll = "m_LimitAll"
)

// Signature algorithm identifiers, likewise the contents of the
// kSecKeyAlgorithm* constants.
const (
	algRSAPKCS1SHA1    = "algid:sign:RSA:digest-PKCS1v15:SHA1"
	algRSAPKCS1SHA256  = "algid:sign:RSA:digest-PKCS1v15:SHA256"
	algRSAPKCS1SHA384  = "algid:sign:RSA:digest-PKCS1v15:SHA384"
	algRSAPKCS1SHA512  = "algid:sign:RSA:digest-PKCS1v15:SHA512"
	algECDSAX962SHA256 = "algid:sign:ECDSA:digest-X962:SHA256"
	algECDSAX962SHA384 = "algid:sign:ECDSA:digest-X962:SHA384"
	algECDSAX962SHA512 = "algid:sign:ECDSA:digest-X962:SHA512"
)

// identityQuery asks for every identity the Keychain holds.
func identityQuery() objc.ID {
	return cocoa.Dict(
		cocoa.String(kSecClass), cocoa.String(kSecClassIdentity),
		cocoa.String(kSecReturnRef), cocoa.Bool(true),
		cocoa.String(kSecMatchLimit), cocoa.String(kSecMatchLimitAll),
	)
}

// Identities lists the code-signing identities the Keychain holds.
//
// It exists so that a build can find the right identity rather than requiring
// one to be named. Getting that wrong is not a loud failure: signing ad hoc by
// default produces a bundle that installs, registers and reports itself
// enabled while its extension cannot read a byte of its own configuration.
//
// No prompt is raised. Reading a certificate touches no private key; only
// signing does that.
func Identities() ([]Identity, error) {
	if err := loadSecurity(); err != nil {
		return nil, err
	}

	var (
		out    []Identity
		retErr error
	)
	cocoa.WithPool(func() {
		found, status := copyIdentities()
		if status == errSecItemNotFound || found == 0 {
			return
		}
		if status != errSecSuccess {
			retErr = fmt.Errorf("keychain: listing signing identities: Keychain error %d", status)
			return
		}
		defer cocoa.Release(found)

		for i := range cocoa.Count(found) {
			identity := cocoa.ObjectAt(found, i)

			var certRef objc.ID
			if s := secIdentityCopyCert(identity, &certRef); s != errSecSuccess {
				continue
			}
			der := certificateDER(certRef)
			cocoa.Release(certRef)

			cert, err := x509.ParseCertificate(der)
			if err != nil {
				continue
			}
			id := Identity{Name: cert.Subject.CommonName}
			if len(cert.Subject.OrganizationalUnit) > 0 {
				id.TeamID = cert.Subject.OrganizationalUnit[0]
			}
			out = append(out, id)
		}
	})
	return out, retErr
}

// copyIdentities runs the identity query. found is +1 to the caller.
func copyIdentities() (found objc.ID, status int32) {
	status = secItemCopyMatching(identityQuery(), &found)
	return found, status
}

// Find locates the code-signing identity whose certificate's common name
// contains name, and returns a Signer backed by it.
//
// Matching is on a substring so that a partial name works, which is what a
// developer actually has to hand: "Apple Development" alone is enough when
// there is one such identity. A name matching more than one identity is an error
// that lists them, never a guess.
//
// Every identity is fetched and filtered in Go rather than trusting the
// Keychain's own matching. kSecMatchSubjectContains is not honoured for
// identity queries: a query naming a certificate that does not exist still
// returns one. Relying on it would mean signing with whichever identity came
// first, silently and with the wrong Team ID, which is the sort of mistake only
// discovered after distribution.
//
// Find performs one signature to settle the Keychain's access control (see the
// package documentation), so it can raise an authorisation prompt. If that
// signature fails, Find fails: signing with the key would fail the same way.
// The caller owns the Signer and must Close it.
func Find(name string) (*Signer, error) {
	if name == "" {
		return nil, errors.New("keychain: a signing identity name is required")
	}
	if err := loadSecurity(); err != nil {
		return nil, err
	}

	var (
		signer *Signer
		err    error
	)
	cocoa.WithPool(func() {
		signer, err = findIdentity(name)
	})
	return signer, err
}

func findIdentity(name string) (*Signer, error) {
	found, status := copyIdentities()
	if status == errSecItemNotFound || found == 0 {
		return nil, errors.New("keychain: no code-signing identities are available in the Keychain; " +
			"`security find-identity -v -p codesigning` lists what is installed")
	}
	if status != errSecSuccess {
		return nil, fmt.Errorf("keychain: listing signing identities: Keychain error %d", status)
	}
	defer cocoa.Release(found)

	type candidate struct {
		identity objc.ID
		cert     *x509.Certificate
		certRef  objc.ID // +1, released below
	}
	var (
		matches []candidate
		names   []string
	)
	for i := range cocoa.Count(found) {
		identity := cocoa.ObjectAt(found, i)

		var certRef objc.ID
		if s := secIdentityCopyCert(identity, &certRef); s != errSecSuccess {
			continue
		}
		cert, err := x509.ParseCertificate(certificateDER(certRef))
		if err != nil {
			cocoa.Release(certRef)
			continue
		}

		names = append(names, cert.Subject.CommonName)
		if strings.Contains(cert.Subject.CommonName, name) {
			matches = append(matches, candidate{identity: identity, cert: cert, certRef: certRef})
			continue
		}
		cocoa.Release(certRef)
	}
	releaseAll := func() {
		for _, m := range matches {
			cocoa.Release(m.certRef)
		}
	}

	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("keychain: no code-signing identity matches %q; the Keychain holds: %s",
			name, strings.Join(names, ", "))
	case 1:
	default:
		var ambiguous []string
		for _, m := range matches {
			ambiguous = append(ambiguous, m.cert.Subject.CommonName)
		}
		releaseAll()
		return nil, fmt.Errorf("keychain: %q matches more than one signing identity, so it is ambiguous: %s",
			name, strings.Join(ambiguous, ", "))
	}

	m := matches[0]
	defer cocoa.Release(m.certRef)

	var keyRef objc.ID
	if s := secIdentityCopyPrivateKey(m.identity, &keyRef); s != errSecSuccess {
		return nil, fmt.Errorf("keychain: the identity %q has no usable private key: Keychain error %d",
			m.cert.Subject.CommonName, s)
	}

	// keyRef is retained for the signer's lifetime and released by Close.
	signer := &Signer{cert: m.cert, key: keyRef, chain: buildChain(m.certRef, m.cert)}
	if err := signer.authorise(); err != nil {
		signer.release()
		return nil, fmt.Errorf("keychain: the Keychain would not let %q sign: %w", m.cert.Subject.CommonName, err)
	}
	return signer, nil
}

// certificateDER returns a certificate's DER encoding. The data object that
// SecCertificateCopyData hands back is +1; it is autoreleased here so that it is
// freed with the surrounding pool.
func certificateDER(cert objc.ID) []byte {
	data := secCertificateCopyData(cert)
	if data == 0 {
		return nil
	}
	return cocoa.GoBytes(cocoa.Autorelease(data))
}

// authorise performs one throwaway signature so the Keychain's access control
// is settled before any real work begins.
//
// Signing a bundle takes several signature operations, each of which consults
// the key's access control. When the calling program is not yet on the key's
// list, all of them raise a prompt at once and the user faces a stack of
// dialogs. Clicking "Always Allow" on one does not dismiss the others: they were
// already in flight, and each was authorised against the list as it stood when
// it started. One signature first, allowed to finish, means exactly one prompt;
// by the time the real operations run the program is on the list and they
// proceed silently.
func (s *Signer) authorise() error {
	digest := sha256.Sum256([]byte("macbundle keychain authorisation probe"))
	_, err := s.Sign(nil, digest[:], crypto.SHA256)
	return err
}

// buildChain resolves the certificate chain through the system's trust
// evaluation.
//
// Apple's tooling expects the leaf and its intermediates; a signature carrying
// only the leaf verifies locally but is incomplete for distribution. Trust
// evaluation is used rather than searching the Keychain by issuer because it is
// the mechanism that already knows where Apple's intermediates live.
//
// A failure here is not fatal: the leaf alone still carries the Team ID, which
// is what local development needs.
func buildChain(certRef objc.ID, leaf *x509.Certificate) []*x509.Certificate {
	chain := []*x509.Certificate{leaf}

	policy := secPolicyCreateBasicX509()
	if policy == 0 {
		return chain
	}
	defer cocoa.Release(policy)

	var trust objc.ID
	if status := secTrustCreateWithCerts(cocoa.Array([]objc.ID{certRef}), policy, &trust); status != errSecSuccess || trust == 0 {
		return chain
	}
	defer cocoa.Release(trust)

	// The result is deliberately ignored: this answers a bool, not an error, and
	// an expired or untrusted certificate still yields its chain. Whether it is
	// trusted is not this function's question. The CFError written on failure is
	// +1 to us, so it is released rather than leaked once per chain built.
	var evalErr objc.ID
	if !secTrustEvaluateWithError(trust, &evalErr) {
		cocoa.Release(evalErr)
	}

	certs := secTrustCopyCertChain(trust)
	if certs == 0 {
		return chain
	}
	defer cocoa.Release(certs)

	out := make([]*x509.Certificate, 0, cocoa.Count(certs))
	for i := range cocoa.Count(certs) {
		parsed, err := x509.ParseCertificate(certificateDER(cocoa.ObjectAt(certs, i)))
		if err != nil {
			continue
		}
		out = append(out, parsed)
	}
	if len(out) == 0 {
		return chain
	}
	return out
}

// Public returns the signing certificate's public key.
func (s *Signer) Public() crypto.PublicKey { return s.cert.PublicKey }

// Certificates returns the leaf followed by any intermediates the system could
// resolve.
func (s *Signer) Certificates() []*x509.Certificate { return s.chain }

// Certificate returns the leaf.
func (s *Signer) Certificate() *x509.Certificate { return s.cert }

// Close releases the Keychain key reference. It is safe to call more than once,
// and waits for any signature in progress.
func (s *Signer) Close() error {
	s.release()
	return nil
}

func (s *Signer) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key != 0 {
		cocoa.Release(s.key)
		s.key = 0
	}
}

// signCount records how many signature operations were performed, which is the
// number of times the Keychain's access control was consulted. A test uses it
// to check that Find settles that before returning.
var signCount atomic.Int64

// Sign produces a signature over an already-computed digest.
//
// This is exactly the shape SecKeyCreateSignature expects with a "digest-"
// algorithm, so the digest passes straight through and the private key is never
// materialised in this process.
//
// rand is ignored: the Security framework supplies its own randomness, and
// crypto.Signer permits implementations backed by hardware or a system service
// to do so.
func (s *Signer) Sign(_ io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.key == 0 {
		return nil, errors.New("keychain: the signer has been closed")
	}
	alg, err := s.algorithmFor(opts.HashFunc())
	if err != nil {
		return nil, err
	}

	signCount.Add(1)
	var (
		sig    []byte
		sigErr error
	)
	cocoa.WithPool(func() {
		var cfErr objc.ID
		out := secKeyCreateSignature(s.key, cocoa.String(alg), cocoa.Data(digest), &cfErr)
		if out == 0 {
			sigErr = fmt.Errorf("keychain: the Keychain refused to sign: %s", cfErrorMessage(cfErr))
			cocoa.Release(cfErr)
			return
		}
		sig = cocoa.GoBytes(out)
		cocoa.Release(out)
	})
	return sig, sigErr
}

// algorithmFor maps a hash to the Security framework's algorithm identifier for
// this key type.
func (s *Signer) algorithmFor(h crypto.Hash) (string, error) {
	switch s.cert.PublicKeyAlgorithm {
	case x509.RSA:
		switch h {
		case crypto.SHA1:
			return algRSAPKCS1SHA1, nil
		case crypto.SHA256:
			return algRSAPKCS1SHA256, nil
		case crypto.SHA384:
			return algRSAPKCS1SHA384, nil
		case crypto.SHA512:
			return algRSAPKCS1SHA512, nil
		}
	case x509.ECDSA:
		switch h {
		case crypto.SHA256:
			return algECDSAX962SHA256, nil
		case crypto.SHA384:
			return algECDSAX962SHA384, nil
		case crypto.SHA512:
			return algECDSAX962SHA512, nil
		}
	}
	return "", fmt.Errorf("keychain: no Security framework algorithm for a %v key with %v",
		s.cert.PublicKeyAlgorithm, h)
}

// cfErrorMessage renders a CFErrorRef, which is toll-free bridged to NSError.
func cfErrorMessage(err objc.ID) string {
	const unknown = "no detail was reported"
	if err == 0 {
		return unknown
	}
	if msg := cstrings.NSStringToString(err.Send(objc.RegisterName("localizedDescription"))); msg != "" {
		return msg
	}
	return unknown
}
