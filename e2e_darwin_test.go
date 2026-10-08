//go:build darwin

package darwinbundle_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/TotallyGamerJet/darwinbundle"
	"github.com/TotallyGamerJet/darwinbundle/keychain"
)

func needKeychain(t *testing.T) {
	t.Helper()
	if keychainEnv == nil {
		t.Skip("set DARWINBUNDLE_TEST_KEYCHAIN=1 to test against a throwaway keychain")
	}
}

func TestSigningWithAKeychainIdentity(t *testing.T) {
	needKeychain(t)
	signer, err := keychain.Find(keychainEnv.Names[0])
	if err != nil {
		t.Fatalf("keychain.Find: %v", err)
	}
	defer func() {
		if err := signer.Close(); err != nil {
			t.Error(err)
		}
	}()

	bin := copyOf(t, thinBinary(t, "arm64"))
	if err := darwinbundle.Sign(bin, darwinbundle.SignConfig{Signer: signer, Identifier: "com.example.keychain"}); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	sig, err := darwinbundle.Inspect(context.Background(), bin)
	if err != nil {
		t.Fatal(err)
	}
	if sig.TeamID != testTeam || sig.AdHoc || sig.Identifier != "com.example.keychain" {
		t.Errorf("signature = %+v", sig)
	}
	if res, err := darwinbundle.Verify(context.Background(), bin, darwinbundle.VerifyOptions{}); err != nil {
		t.Errorf("the signature does not verify: %v\n%s", err, res.Output)
	}
}

// TestReleaseBuildEndToEnd runs the pipeline a release build is: universal
// binaries, a bundle with an extension, signed from the inside out with a
// Keychain identity, verified, archived, and verified again after unpacking.
//
// The last step is the one that matters. A signature that is valid where it was
// made and invalid on the far side of an archive is a release that fails on
// every user's machine and passes on the build host.
func TestReleaseBuildEndToEnd(t *testing.T) {
	needKeychain(t)
	ctx := context.Background()
	dir := t.TempDir()

	universal := func(name string) string {
		out := filepath.Join(dir, "bin", name)
		if err := darwinbundle.MakeUniversal(out, thinBinary(t, "arm64"), thinBinary(t, "amd64")); err != nil {
			t.Fatal(err)
		}
		return out
	}
	b := app(t)
	b.Executable = universal("app")
	b.PlugIns[0].Executable = universal("ext")
	b.IconSet = iconSet(t)

	out := filepath.Join(dir, "out")
	appPath := build(t, b, out)

	signer, err := keychain.Find(keychainEnv.Names[0])
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := signer.Close(); err != nil {
			t.Error(err)
		}
	}()
	cfg := darwinbundle.SignConfig{Signer: signer}

	// From the inside out: the application's seal covers the extension's
	// signature, so the extension is signed first.
	if err := darwinbundle.Sign(b.PlugIns[0].Path(b.PlugInsDir(out)), cfg); err != nil {
		t.Fatalf("signing the extension: %v", err)
	}
	if err := darwinbundle.Sign(appPath, cfg); err != nil {
		if errors.Is(err, darwinbundle.ErrNestedBundlesUnsupported) {
			t.Skip("this quill cannot sign nested bundles; see ErrNestedBundlesUnsupported")
		}
		t.Fatalf("signing the application: %v", err)
	}
	if res, err := darwinbundle.Verify(ctx, appPath, darwinbundle.VerifyOptions{Deep: true}); err != nil {
		t.Fatalf("the signed application does not verify: %v\n%s", err, res.Output)
	}

	// Archive, unpack somewhere else, verify the copy.
	zipPath := filepath.Join(dir, "App.zip")
	if err := darwinbundle.Zip(zipPath, appPath); err != nil {
		t.Fatalf("Zip: %v", err)
	}
	unpacked := filepath.Join(dir, "unpacked")
	if err := os.MkdirAll(unpacked, 0o755); err != nil {
		t.Fatal(err)
	}
	if o, err := exec.Command("unzip", "-q", zipPath, "-d", unpacked).CombinedOutput(); err != nil {
		t.Fatalf("unzip: %v\n%s", err, o)
	}
	copyPath := filepath.Join(unpacked, filepath.Base(appPath))
	if res, err := darwinbundle.Verify(ctx, copyPath, darwinbundle.VerifyOptions{Deep: true}); err != nil {
		t.Fatalf("the unpacked application does not verify: %v\n%s", err, res.Output)
	}

	// And it is signed by the identity that signed it, in every piece.
	for _, p := range []string{copyPath, b.PlugIns[0].Path(b.PlugInsDir(unpacked))} {
		sig, err := darwinbundle.Inspect(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		if sig.TeamID != testTeam || !sig.HardenedRuntime {
			t.Errorf("%s: %+v", filepath.Base(p), sig)
		}
	}
}
