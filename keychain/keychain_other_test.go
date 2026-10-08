//go:build !darwin

package keychain

import (
	"crypto"
	"errors"
	"testing"
)

// Off macOS the package must still compile and must say plainly why it cannot
// work, so a program can import it unconditionally and decide at run time.
func TestOffMacOSNothingWorksAndSaysSo(t *testing.T) {
	if _, err := Find("anything"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Find: got %v, want ErrUnsupported", err)
	}
	if _, err := Identities(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Identities: got %v, want ErrUnsupported", err)
	}
	var s Signer
	if _, err := s.Sign(nil, nil, crypto.SHA256); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Sign: got %v, want ErrUnsupported", err)
	}
	if s.Close() != nil {
		t.Error("Close on the placeholder failed")
	}
}
