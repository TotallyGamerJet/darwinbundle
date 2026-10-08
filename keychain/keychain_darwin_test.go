//go:build darwin

package keychain

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/sha256"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TotallyGamerJet/macbundle/internal/keychaintest"
)

func TestFindLocatesTheIdentity(t *testing.T) {
	s, err := Find(identityName(t))
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	defer closeSigner(t, s)

	cert := s.Certificate()
	if cert == nil {
		t.Fatal("no certificate was returned")
	}
	if cert.Subject.CommonName == "" {
		t.Error("the certificate has no common name")
	}
	// The Team ID lives in the organizational unit, and it is what the sandbox
	// checks a team-prefixed App Group against.
	if len(cert.Subject.OrganizationalUnit) == 0 {
		t.Error("the certificate carries no Team ID")
	}
	if testEnv != nil && cert.Subject.OrganizationalUnit[0] != keychaintest.Team {
		t.Errorf("team = %v, want %s", cert.Subject.OrganizationalUnit, keychaintest.Team)
	}
}

func TestFindMatchesAPartialName(t *testing.T) {
	if testEnv == nil {
		t.Skip("needs the throwaway keychain, which holds a name unique to this test")
	}
	// Part of the name, from the middle of it: this run's unique prefix and the
	// word that tells the two identities apart.
	s, err := Find(testEnv.Prefix + " one")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	defer closeSigner(t, s)
	if got := s.Certificate().Subject.CommonName; got != testEnv.Names[0] {
		t.Errorf("found %q, want %q", got, testEnv.Names[0])
	}
}

// The Keychain does not honour kSecMatchSubjectContains for identity queries: a
// query naming a certificate that does not exist still returns one. Before the
// filtering moved into Go, a nonsense name returned a signer, which in a build
// meant an artefact signed by whichever identity came first, with the wrong Team
// ID, discovered only after distribution.
func TestUnknownIdentityIsReportedClearly(t *testing.T) {
	identityName(t) // skips when there is no keychain to ask
	s, err := Find("no such identity exists anywhere 0xDEADBEEF")
	if err == nil {
		closeSigner(t, s)
		t.Fatal("a nonsense name produced a signer")
	}
	if !strings.Contains(err.Error(), "no code-signing identity matches") {
		t.Errorf("error does not say what happened: %v", err)
	}
}

func TestAmbiguousIdentityIsRejected(t *testing.T) {
	if testEnv == nil {
		t.Skip("needs the throwaway keychain, which holds two similar identities")
	}
	s, err := Find(testEnv.Prefix)
	if err == nil {
		closeSigner(t, s)
		t.Fatal("a name matching two identities was accepted")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error does not say it is ambiguous: %v", err)
	}
	// It must name the candidates so the caller can be more specific.
	for _, name := range testEnv.Names {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not list %q: %v", name, err)
		}
	}
}

func TestEmptyIdentityIsRejected(t *testing.T) {
	if _, err := Find(""); err == nil {
		t.Error("an empty name was accepted")
	}
}

func TestSignerProducesAVerifiableSignature(t *testing.T) {
	s, err := Find(identityName(t))
	if err != nil {
		t.Fatal(err)
	}
	defer closeSigner(t, s)

	digest := sha256.Sum256([]byte("a message worth signing"))
	sig, err := s.Sign(nil, digest[:], crypto.SHA256)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if len(sig) == 0 {
		t.Fatal("empty signature")
	}

	// Verified in Go against the certificate's public key, which is the
	// independent check: the Security framework produced the signature, and
	// crypto/ecdsa — which knows nothing about the Keychain — accepts it.
	pub, ok := s.Certificate().PublicKey.(*ecdsa.PublicKey)
	if !ok {
		t.Skipf("the identity holds a %T key; this check is for ECDSA", s.Certificate().PublicKey)
	}
	if !ecdsa.VerifyASN1(pub, digest[:], sig) {
		t.Error("the signature does not verify against the certificate's public key")
	}
	tampered := digest
	tampered[0] ^= 0xFF
	if ecdsa.VerifyASN1(pub, tampered[:], sig) {
		t.Error("the signature verifies for a different digest")
	}
}

func TestPublicKeyMatchesTheCertificate(t *testing.T) {
	s, err := Find(identityName(t))
	if err != nil {
		t.Fatal(err)
	}
	defer closeSigner(t, s)
	if got, want := s.Public(), s.Certificate().PublicKey; !publicKeysEqual(got, want) {
		t.Error("Public does not return the certificate's key")
	}
}

func publicKeysEqual(a, b crypto.PublicKey) bool {
	type equaler interface{ Equal(crypto.PublicKey) bool }
	if e, ok := a.(equaler); ok {
		return e.Equal(b)
	}
	return false
}

func TestSignerReportsTheChainLeafFirst(t *testing.T) {
	s, err := Find(identityName(t))
	if err != nil {
		t.Fatal(err)
	}
	defer closeSigner(t, s)

	chain := s.Certificates()
	if len(chain) == 0 {
		t.Fatal("no certificates")
	}
	// Leaf first is the order macbundle.Signer promises. For a self-signed
	// identity the chain is just the leaf, so this also catches a signer that
	// returned an issuer where the leaf belongs.
	if !chain[0].Equal(s.Certificate()) {
		t.Errorf("the first certificate is %q, not the leaf %q",
			chain[0].Subject.CommonName, s.Certificate().Subject.CommonName)
	}
}

func TestClosedSignerRefusesToSign(t *testing.T) {
	s, err := Find(identityName(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("x"))
	if _, err := s.Sign(nil, digest[:], crypto.SHA256); err == nil {
		t.Error("a closed signer signed")
	}
	// Closing twice is not a crash.
	if err := s.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestAnUnsupportedHashIsRefusedBeforeTheKeychainIsAsked(t *testing.T) {
	s, err := Find(identityName(t))
	if err != nil {
		t.Fatal(err)
	}
	defer closeSigner(t, s)
	before := signCount.Load()
	if _, err := s.Sign(nil, make([]byte, 28), crypto.SHA224); err == nil {
		t.Error("SHA-224 was accepted")
	}
	if signCount.Load() != before {
		t.Error("the Keychain was asked to sign with an algorithm there is no identifier for")
	}
}

// Signing a bundle takes several signature operations, each of which consults
// the key's access control. With the calling program not yet on the key's list,
// all of them prompt at once, and the user faces a stack of dialogs of which
// "Always Allow" dismisses one. One signature when the signer is created means
// one prompt.
func TestFindSettlesAccessControlUpFront(t *testing.T) {
	name := identityName(t)
	before := signCount.Load()

	s, err := Find(name)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSigner(t, s)

	if signCount.Load() <= before {
		t.Error("no signature was performed at creation, so the first real signing " +
			"operation would be the one that prompts, alongside the others")
	}
}

// Close and Sign race in any program that signs concurrently and closes on the
// way out. Run under -race this is the check that the lock is the right one.
func TestSignAndCloseDoNotRace(t *testing.T) {
	s, err := Find(identityName(t))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("x"))

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Success, or having lost the race to Close, are both fine. Anything
			// else — and above all a crash — is not.
			if _, err := s.Sign(nil, digest[:], crypto.SHA256); err != nil &&
				!strings.Contains(err.Error(), "has been closed") {
				t.Errorf("Sign failed for a reason other than the race: %v", err)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	wg.Wait()
}

func TestIdentitiesReportTeams(t *testing.T) {
	ids, err := Identities()
	if err != nil {
		t.Skipf("the Keychain could not be queried: %v", err)
	}
	if len(ids) == 0 {
		t.Skip("no code-signing identities are installed")
	}
	for _, id := range ids {
		if id.Name == "" {
			t.Error("an identity came back with no common name")
		}
		// A Team ID is exactly ten characters when present. Apple's own platform
		// certificates legitimately have none, so empty is allowed and only a
		// malformed value is a fault.
		if id.TeamID != "" && len(id.TeamID) != 10 {
			t.Errorf("identity %q has team %q, which is not a Team ID", id.Name, id.TeamID)
		}
	}
	if testEnv != nil {
		found := 0
		for _, id := range ids {
			for _, name := range testEnv.Names {
				if id.Name == name && id.TeamID == keychaintest.Team {
					found++
				}
			}
		}
		if found != 2 {
			t.Errorf("found %d of the 2 test identities, with their team, in %+v", found, ids)
		}
	}
}

// Listing identities must not raise a Keychain prompt: it reads certificates,
// which are public, and never touches a private key. A prompt would block every
// build on a dialog, so the signal is that it returns promptly.
func TestIdentitiesDoNotPrompt(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := Identities(); err != nil {
			t.Errorf("Identities: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("listing identities blocked; it may be raising a Keychain prompt")
	}
}

func closeSigner(t *testing.T, s *Signer) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
