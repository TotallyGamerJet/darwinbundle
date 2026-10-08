//go:build !darwin

package darwinbundle_test

// platformSetup has nothing to set up off macOS: there is no Keychain.
func platformSetup() (teardown func() error, err error) {
	return func() error { return nil }, nil
}
