//go:build darwin

// Package keychaintest builds a throwaway Keychain holding generated
// code-signing identities, so that tests can exercise the real Security
// framework without touching, or prompting for, the developer's own keychain.
//
// It is opt-in. Putting a keychain on the search list changes machine state for
// the duration of a test run: the list is restored on the way out, but a run
// killed part-way leaves a dangling entry, and that is a decision a developer
// should make rather than have made for them. CI makes it by setting
// MACBUNDLE_TEST_KEYCHAIN=1.
package keychaintest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"software.sslmate.com/src/go-pkcs12"
)

// Team is the Team ID carried by every identity this package creates.
const Team = "TEAM123456"

// Env is a live throwaway keychain.
type Env struct {
	// Names are the common names of the identities in it, "macbundle keychain
	// test one (TEAM123456)" and "... two ...": similar enough that a search for
	// the shared prefix is ambiguous, different enough to tell apart.
	Names [2]string

	// Prefix matches both names.
	Prefix string

	dir      string
	keychain string
	original []string
}

// Enabled reports whether the environment asks for a throwaway keychain.
func Enabled() bool { return os.Getenv("MACBUNDLE_TEST_KEYCHAIN") == "1" }

// Setup creates the keychain, imports two identities, and puts it first on the
// user's search list. Call Close when finished, whatever happened in between.
func Setup() (env *Env, err error) {
	dir, err := os.MkdirTemp("", "macbundle-keychain-")
	if err != nil {
		return nil, err
	}
	env = &Env{
		dir:      dir,
		keychain: filepath.Join(dir, "test.keychain-db"),
		Prefix:   "macbundle keychain test",
		Names: [2]string{
			"macbundle keychain test one (" + Team + ")",
			"macbundle keychain test two (" + Team + ")",
		},
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, env.Close())
		}
	}()

	password := randomHex(16)
	for _, args := range [][]string{
		{"create-keychain", "-p", password, env.keychain},
		// Do not lock again for an hour, so a slow test run is not interrupted.
		{"set-keychain-settings", "-t", "3600", env.keychain},
		{"unlock-keychain", "-p", password, env.keychain},
	} {
		if err := security(args...); err != nil {
			return env, err
		}
	}

	for _, name := range env.Names {
		p12, err := identityP12(name)
		if err != nil {
			return env, err
		}
		file := filepath.Join(dir, strings.Fields(name)[3]+".p12")
		if err := os.WriteFile(file, p12, 0o600); err != nil {
			return env, err
		}
		// -A lets any application use the key without a prompt. That is a
		// reasonable thing to do to a key generated a moment ago for a test, and
		// the only way a test run does not stop at a dialog.
		if err := security("import", file, "-k", env.keychain, "-P", "pw", "-A", "-f", "pkcs12"); err != nil {
			return env, err
		}
	}
	if err := security("set-key-partition-list", "-S", "apple-tool:,apple:,codesign:", "-s",
		"-k", password, env.keychain); err != nil {
		return env, err
	}

	env.original, err = searchList()
	if err != nil {
		return env, err
	}
	// Ahead of the login keychain so the test identities are found first, and
	// the login keychain stays on the list so nothing else breaks.
	if err := security(append([]string{"list-keychains", "-d", "user", "-s", env.keychain}, env.original...)...); err != nil {
		return env, err
	}
	return env, nil
}

// Close restores the search list and deletes the keychain. It reports every
// failure rather than the first: a half-cleaned machine is the thing to know
// about.
func (e *Env) Close() error {
	var errs []error
	if e.original != nil {
		errs = append(errs, security(append([]string{"list-keychains", "-d", "user", "-s"}, e.original...)...))
	}
	if _, err := os.Stat(e.keychain); err == nil {
		errs = append(errs, security("delete-keychain", e.keychain))
	}
	errs = append(errs, os.RemoveAll(e.dir))
	return errors.Join(errs...)
}

// identityP12 generates a self-signed code-signing identity as a PKCS#12 file.
//
// The legacy encoding is deliberate: macOS's importer rejects the stronger
// ciphers modern tooling defaults to, with a MAC verification error that says
// nothing about ciphers.
func identityP12(commonName string) ([]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:         commonName,
			OrganizationalUnit: []string{Team},
			Organization:       []string{"macbundle tests"},
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(48 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return pkcs12.LegacyDES.Encode(key, cert, nil, "pw")
}

func security(args ...string) error {
	out, err := exec.Command("/usr/bin/security", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("security %s: %w\n%s", args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}

// searchList returns the user's keychain search list as paths.
func searchList() ([]string, error) {
	out, err := exec.Command("/usr/bin/security", "list-keychains", "-d", "user").Output()
	if err != nil {
		return nil, fmt.Errorf("security list-keychains: %w", err)
	}
	var list []string
	for line := range strings.SplitSeq(string(out), "\n") {
		if p := strings.Trim(strings.TrimSpace(line), `"`); p != "" {
			list = append(list, p)
		}
	}
	return list, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on a working system
	}
	return hex.EncodeToString(b)
}
