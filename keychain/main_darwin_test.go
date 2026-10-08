//go:build darwin

package keychain

import (
	"fmt"
	"os"
	"testing"

	"github.com/TotallyGamerJet/macbundle/internal/keychaintest"
)

// testEnv is the throwaway keychain, or nil when the tests are not asked to
// build one. SIGNING_IDENTITY names an identity in the developer's own keychain
// instead; using it raises Keychain prompts, which is why neither route is the
// default.
var testEnv *keychaintest.Env

func TestMain(m *testing.M) {
	if keychaintest.Enabled() {
		env, err := keychaintest.Setup()
		if err != nil {
			fmt.Fprintln(os.Stderr, "setting up the test keychain:", err)
			if env != nil {
				if cerr := env.Close(); cerr != nil {
					fmt.Fprintln(os.Stderr, "and cleaning up after it:", cerr)
				}
			}
			os.Exit(1)
		}
		testEnv = env
	}

	code := m.Run()

	if testEnv != nil {
		if err := testEnv.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "cleaning up the test keychain:", err)
			if code == 0 {
				code = 1
			}
		}
	}
	os.Exit(code)
}

// identityName is the identity to sign with: the throwaway keychain's first, or
// the one named by SIGNING_IDENTITY, or the test is skipped.
func identityName(t *testing.T) string {
	t.Helper()
	if testEnv != nil {
		return testEnv.Names[0]
	}
	if name := os.Getenv("SIGNING_IDENTITY"); name != "" {
		return name
	}
	t.Skip("set MACBUNDLE_TEST_KEYCHAIN=1 to test against a throwaway keychain, " +
		"or SIGNING_IDENTITY to use one of your own (which will prompt)")
	return ""
}
