package macbundle

import (
	"crypto/x509"
	"errors"
	"testing"
)

// TestQuillChainOrder pins an adaptation whose absence is silent.
//
// quill's SigningMaterial.Leaf takes the last certificate and returns nil if it
// is a CA, so it wants the leaf last. Signer.Certificates promises the
// conventional opposite. Getting it wrong produces a signature that verifies
// perfectly but carries no Team ID, and is then refused access to a
// team-prefixed App Group with nothing anywhere explaining why.
func TestQuillChainOrder(t *testing.T) {
	leaf := &x509.Certificate{IsCA: false}
	intermediate := &x509.Certificate{IsCA: true}
	root := &x509.Certificate{IsCA: true}

	got := quillChainOrder([]*x509.Certificate{leaf, intermediate, root})

	if len(got) != 3 {
		t.Fatalf("got %d certificates, want 3", len(got))
	}
	if got[len(got)-1] != leaf {
		t.Error("the leaf is not last, so quill would read a CA and omit the Team ID")
	}
	if got[0] != root {
		t.Error("the root is not first")
	}
}

func TestQuillChainOrderEdges(t *testing.T) {
	leaf := &x509.Certificate{}
	if got := quillChainOrder([]*x509.Certificate{leaf}); len(got) != 1 || got[0] != leaf {
		t.Error("a single-certificate chain was not preserved")
	}
	if got := quillChainOrder(nil); len(got) != 0 {
		t.Errorf("an empty chain produced %d entries", len(got))
	}
}

// The input must not be reordered in place: the caller's slice is the signer's
// own, and mutating it would reverse the chain on every second call.
func TestQuillChainOrderDoesNotModifyItsInput(t *testing.T) {
	a, b := &x509.Certificate{}, &x509.Certificate{}
	in := []*x509.Certificate{a, b}
	quillChainOrder(in)
	if in[0] != a || in[1] != b {
		t.Error("the input chain was reordered")
	}
}

// The refusal text is quill's, copied from what it printed when asked to sign
// the application in TestSignAnApplicationWithAnExtension without the fix.
func TestTheNestedBundleRefusalIsRecognised(t *testing.T) {
	refusal := errors.New(`unable to seal bundle resources: signing nested bundles is not supported ` +
		`(found "PlugIns/TestExt.appex"): sign it separately before signing this bundle`)
	if !isNestedBundleRefusal(refusal) {
		t.Error("quill's refusal to sign a nested bundle was not recognised")
	}
	if isNestedBundleRefusal(errors.New("the keychain refused to sign")) {
		t.Error("an unrelated failure was taken for the nested-bundle refusal")
	}
}
