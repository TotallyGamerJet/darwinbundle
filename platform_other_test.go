//go:build !darwin

package macbundle_test

// platformSetup has nothing to set up off macOS: there is no Keychain.
func platformSetup() (teardown func() error, err error) {
	return func() error { return nil }, nil
}
