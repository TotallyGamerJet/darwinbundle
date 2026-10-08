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
//
// # Concurrency
//
// go test runs packages in parallel, and more than one of this module's uses a
// keychain. They would each read the search list, add their own, and write it
// back, so one's cleanup could restore a list still naming the other's deleted
// keychain, or remove a keychain the other is mid-test against. Setup therefore
// takes a lock that spans processes, and holds it until Close: the keychain
// tests of different packages run one after another, not at once.
//
// A run killed outright leaves its keychain on the list and its directory in the
// temp directory. The next Setup removes list entries that no longer exist.
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
	"io/fs"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
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
	lock     *os.File

	// listChanged records that the search list was rewritten, so Close puts it
	// back. It is not the same as original being non-nil: a machine with an empty
	// list has nothing to restore it to, and still needs the entry removed.
	listChanged bool
}

// Enabled reports whether the environment asks for a throwaway keychain.
func Enabled() bool { return os.Getenv("MACBUNDLE_TEST_KEYCHAIN") == "1" }

// Setup creates the keychain, imports two identities, and puts it first on the
// user's search list. Call Close when finished, whatever happened in between.
func Setup() (env *Env, err error) {
	lock, err := acquireLock()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "macbundle-keychain-")
	if err != nil {
		return nil, errors.Join(err, releaseLock(lock))
	}
	// A name unique to this run, so that even a keychain left behind by one that
	// was killed cannot make a search for these identities ambiguous.
	prefix := "macbundle keychain test " + randomHex(3)
	env = &Env{
		dir:      dir,
		lock:     lock,
		keychain: filepath.Join(dir, "test.keychain-db"),
		Prefix:   prefix,
		Names: [2]string{
			prefix + " one (" + Team + ")",
			prefix + " two (" + Team + ")",
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
		file := filepath.Join(dir, strings.Fields(name)[4]+".p12")
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
	// A previous run that was killed leaves its keychain on the list, pointing at
	// a directory that is gone. Carrying it forward would keep it there for good.
	env.original = slices.DeleteFunc(env.original, func(path string) bool {
		if !strings.Contains(path, "macbundle-keychain-") {
			return false
		}
		_, statErr := os.Stat(path)
		return errors.Is(statErr, fs.ErrNotExist)
	})
	// Ahead of the login keychain so the test identities are found first, and
	// the login keychain stays on the list so nothing else breaks.
	if err := security(append([]string{"list-keychains", "-d", "user", "-s", env.keychain}, env.original...)...); err != nil {
		return env, err
	}
	env.listChanged = true
	return env, nil
}

// Close restores the search list and deletes the keychain. It reports every
// failure rather than the first: a half-cleaned machine is the thing to know
// about.
func (e *Env) Close() error {
	var errs []error
	if e.listChanged {
		errs = append(errs, security(append([]string{"list-keychains", "-d", "user", "-s"}, e.original...)...))
	}
	if _, err := os.Stat(e.keychain); err == nil {
		errs = append(errs, security("delete-keychain", e.keychain))
	}
	errs = append(errs, os.RemoveAll(e.dir))
	// Last, so that nothing above overlaps with another process's Setup.
	if e.lock != nil {
		errs = append(errs, releaseLock(e.lock))
	}
	return errors.Join(errs...)
}

// acquireLock takes an exclusive lock that other processes' calls wait on. It
// is advisory and held on an open file, so the kernel drops it if the process
// dies, which is what makes a crashed run unable to wedge the next.
func acquireLock() (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(os.TempDir(), "macbundle-keychain-tests.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("keychaintest: opening the lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return nil, errors.Join(fmt.Errorf("keychaintest: taking the lock: %w", err), f.Close())
	}
	return f, nil
}

func releaseLock(f *os.File) error {
	// Closing releases the lock; the explicit unlock first makes the intent
	// plain and reports a failure that Close alone would not.
	return errors.Join(syscall.Flock(int(f.Fd()), syscall.LOCK_UN), f.Close())
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
