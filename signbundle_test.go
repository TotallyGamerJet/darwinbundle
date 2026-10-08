package darwinbundle_test

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/TotallyGamerJet/darwinbundle"
)

// realApp is app() with genuine Mach-O executables, which signing needs.
func realApp(t testing.TB, withExtension bool) *darwinbundle.Bundle {
	t.Helper()
	b := app(t)
	b.Executable = thinBinary(t, "arm64")
	if withExtension {
		b.PlugIns[0].Executable = thinBinary(t, "arm64")
	} else {
		b.PlugIns = nil
	}
	return b
}

func TestSignAnApplicationBundle(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("checking the result needs codesign")
	}
	b := realApp(t, false)
	dir := t.TempDir()
	path := build(t, b, dir)

	if err := darwinbundle.Sign(path, darwinbundle.SignConfig{Signer: newIdentity(t).signer()}); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if o, err := exec.Command("codesign", "--verify", "--deep", "--strict", "--verbose=2", path).CombinedOutput(); err != nil {
		t.Errorf("codesign rejects the signed application: %s", o)
	}
}

func TestSignAnApplicationWithAnExtension(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("checking the result needs codesign")
	}
	b := realApp(t, true)
	dir := t.TempDir()
	path := build(t, b, dir)
	id := newIdentity(t).signer()

	// From the inside out: the application's seal covers the extension's
	// signature, so the extension has to be signed first.
	if err := darwinbundle.Sign(b.PlugIns[0].Path(b.PlugInsDir(dir)), darwinbundle.SignConfig{Signer: id}); err != nil {
		t.Fatalf("signing the extension: %v", err)
	}
	if err := darwinbundle.Sign(path, darwinbundle.SignConfig{Signer: id}); err != nil {
		if errors.Is(err, darwinbundle.ErrNestedBundlesUnsupported) {
			// Built against a quill without anchore/quill#883, which is how
			// programs importing this module see it unless they replace it. The
			// refusal must at least be recognisable and explain itself.
			if !strings.Contains(err.Error(), "nested bundles") {
				t.Errorf("the refusal does not carry quill's explanation: %v", err)
			}
			t.Skip("this quill cannot sign nested bundles; see ErrNestedBundlesUnsupported")
		}
		t.Fatalf("signing the application: %v", err)
	}
	if o, err := exec.Command("codesign", "--verify", "--deep", "--strict", "--verbose=2", path).CombinedOutput(); err != nil {
		t.Errorf("codesign rejects the signed application: %s", o)
	}
}
