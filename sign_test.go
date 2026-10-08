package darwinbundle_test

import (
	"bytes"
	"crypto/x509"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/TotallyGamerJet/darwinbundle"
)

func TestSignRequiresAnExplicitIdentity(t *testing.T) {
	bin := copyOf(t, thinBinary(t, "arm64"))
	before, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	id := newIdentity(t)

	tests := []struct {
		name string
		cfg  darwinbundle.SignConfig
		want string
	}{
		{"nothing chosen", darwinbundle.SignConfig{}, "names no signing identity"},
		{"only metadata", darwinbundle.SignConfig{Identifier: "x", Entitlements: "e"}, "names no signing identity"},
		{"two identities", darwinbundle.SignConfig{AdHoc: true, P12Path: "x.p12"}, "exactly one"},
		{"all three", darwinbundle.SignConfig{AdHoc: true, P12Path: "x.p12", Signer: id.signer()}, "exactly one"},
		{"a timestamp with ad hoc", darwinbundle.SignConfig{AdHoc: true, TimestampServer: "http://t"}, "needs a certificate"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := darwinbundle.Sign(bin, tc.cfg)
			if err == nil {
				t.Fatal("no error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
	// Refusing must not have touched the file.
	after, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("a rejected configuration modified the binary")
	}
}

func TestSignNamesWhatIsMissing(t *testing.T) {
	err := darwinbundle.Sign(filepath.Join(t.TempDir(), "nothing"), darwinbundle.SignConfig{AdHoc: true})
	if err == nil || !strings.Contains(err.Error(), "nothing to sign") {
		t.Errorf("got %v", err)
	}

	bin := copyOf(t, thinBinary(t, "arm64"))
	err = darwinbundle.Sign(bin, darwinbundle.SignConfig{AdHoc: true, Entitlements: "/nonexistent.plist"})
	if err == nil || !strings.Contains(err.Error(), "entitlements") {
		t.Errorf("got %v", err)
	}

	err = darwinbundle.Sign(bin, darwinbundle.SignConfig{P12Path: filepath.Join(t.TempDir(), "absent.p12")})
	if err == nil || !strings.Contains(err.Error(), "signing certificate") {
		t.Errorf("got %v", err)
	}
}

func TestAdHocSignature(t *testing.T) {
	bin := copyOf(t, thinBinary(t, "arm64"))
	before, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := darwinbundle.Sign(bin, darwinbundle.SignConfig{AdHoc: true}); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	after, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(before, after) {
		t.Error("signing left the file unchanged")
	}
	// Still a Mach-O for the same architecture.
	if arches, err := darwinbundle.Architectures(bin); err != nil || len(arches) != 1 || arches[0] != "arm64" {
		t.Errorf("Architectures after signing = %v, %v", arches, err)
	}

	if runtime.GOOS != "darwin" {
		return
	}
	out, err := exec.Command("codesign", "-d", "-vv", bin).CombinedOutput()
	if err != nil {
		t.Fatalf("codesign -d: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Signature=adhoc") || strings.Contains(string(out), "linker-signed") {
		t.Errorf("not the ad hoc signature quill writes (the linker's is flagged linker-signed):\n%s", out)
	}
	if o, err := exec.Command("codesign", "--verify", "--strict", bin).CombinedOutput(); err != nil {
		t.Errorf("the ad hoc signature does not verify: %s", o)
	}
}

func TestSigningAUniversalBinarySignsEverySlice(t *testing.T) {
	out := filepath.Join(t.TempDir(), "universal")
	if err := darwinbundle.MakeUniversal(out, thinBinary(t, "arm64"), thinBinary(t, "amd64")); err != nil {
		t.Fatal(err)
	}
	if err := darwinbundle.Sign(out, darwinbundle.SignConfig{Signer: newIdentity(t).signer()}); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	arches, err := darwinbundle.Architectures(out)
	if err != nil || len(arches) != 2 {
		t.Errorf("Architectures after signing = %v, %v; want both slices kept", arches, err)
	}

	if runtime.GOOS != "darwin" {
		return
	}
	// Per architecture: codesign without --arch checks only the slice matching
	// the host, so a broken Intel slice verifies clean on Apple Silicon.
	for _, arch := range []string{"arm64", "x86_64"} {
		if o, err := exec.Command("codesign", "--verify", "--strict", "--arch", arch, out).CombinedOutput(); err != nil {
			t.Errorf("the %s slice does not verify: %s", arch, o)
		}
	}
}

// Signing with a PKCS#12 identity and with a Signer must agree about the one
// thing that matters: the Team ID reaches the signature.
func TestEverySourceOfAnIdentityCarriesTheTeam(t *testing.T) {
	id := newIdentity(t)
	sources := map[string]darwinbundle.SignConfig{
		"a PKCS#12 file": {P12Path: id.p12(t, "secret"), P12Password: "secret"},
		"a Signer":       {Signer: id.signer()},
	}
	for name, cfg := range sources {
		t.Run(name, func(t *testing.T) {
			bin := copyOf(t, thinBinary(t, "arm64"))
			if err := darwinbundle.Sign(bin, cfg); err != nil {
				t.Fatalf("Sign: %v", err)
			}
			if runtime.GOOS != "darwin" {
				t.Skip("reading the team back needs codesign")
			}
			out, err := exec.Command("codesign", "-d", "-vv", bin).CombinedOutput()
			if err != nil {
				t.Fatalf("codesign -d: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "TeamIdentifier="+testTeam) {
				t.Errorf("the signature does not carry the team:\n%s", out)
			}
		})
	}
}

func TestAWrongP12PasswordIsReported(t *testing.T) {
	p12 := newIdentity(t).p12(t, "right")
	err := darwinbundle.Sign(copyOf(t, thinBinary(t, "arm64")),
		darwinbundle.SignConfig{P12Path: p12, P12Password: "wrong"})
	if err == nil {
		t.Fatal("signing with the wrong password succeeded")
	}
}

func TestASignerWithNoCertificatesIsRefused(t *testing.T) {
	bin := copyOf(t, thinBinary(t, "arm64"))
	err := darwinbundle.Sign(bin, darwinbundle.SignConfig{Signer: noCerts{newIdentity(t).signer()}})
	if err == nil || !strings.Contains(err.Error(), "no certificates") {
		t.Errorf("got %v", err)
	}
}

type noCerts struct{ *softSigner }

func (noCerts) Certificates() []*x509.Certificate { return nil }

// TestEntitlementsAreEmbedded: what is asked for is what the binary ends up
// declaring, which is read back by codesign rather than assumed.
func TestEntitlementsAreEmbedded(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("reading entitlements back needs codesign")
	}
	ents := filepath.Join(t.TempDir(), "e.plist")
	body := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>com.apple.security.app-sandbox</key><true/></dict></plist>
`
	if err := os.WriteFile(ents, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := copyOf(t, thinBinary(t, "arm64"))
	if err := darwinbundle.Sign(bin, darwinbundle.SignConfig{Signer: newIdentity(t).signer(), Entitlements: ents}); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	out, err := exec.Command("codesign", "-d", "--entitlements", "-", "--xml", bin).Output()
	if err != nil {
		t.Fatalf("codesign: %v", err)
	}
	if !strings.Contains(string(out), "com.apple.security.app-sandbox") {
		t.Errorf("the entitlement is not in the signature:\n%s", out)
	}
}
