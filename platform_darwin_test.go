//go:build darwin

package macbundle_test

import (
	"errors"

	"github.com/TotallyGamerJet/macbundle/internal/keychaintest"
)

// keychainEnv is the throwaway keychain, or nil when the run was not asked to
// build one (MACBUNDLE_TEST_KEYCHAIN=1).
var keychainEnv *keychaintest.Env

func platformSetup() (teardown func() error, err error) {
	if !keychaintest.Enabled() {
		return func() error { return nil }, nil
	}
	env, err := keychaintest.Setup()
	if err != nil {
		if env != nil {
			err = errors.Join(err, env.Close())
		}
		return nil, err
	}
	keychainEnv = env
	return env.Close, nil
}
