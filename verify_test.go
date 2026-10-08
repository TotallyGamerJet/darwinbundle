//go:build unix

package darwinbundle_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/TotallyGamerJet/darwinbundle"
)

// fakeCodesign puts a script named codesign first on PATH. It lets the
// behaviour around the tool — the arguments it is given, what is made of its
// output and exit status, cancellation — be tested on a machine that has no
// codesign, which is the machine this package is most often run on.
//
// body is the script's body. The arguments it was called with are appended to
// the file whose path is returned.
func fakeCodesign(t *testing.T, body string) (argsLog string) {
	t.Helper()
	dir := t.TempDir()
	argsLog = filepath.Join(dir, "args.log")
	script := "#!/bin/sh\necho \"$*\" >> '" + argsLog + "'\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "codesign"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// The system directories stay on the PATH, behind the fake, so the script can
	// use cat and sleep; the fake is first, so it is the codesign that is found.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin:/bin")
	return argsLog
}

func TestWithoutCodesignTheChecksSayWhy(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // nothing in it

	_, err := darwinbundle.Verify(context.Background(), "x", darwinbundle.VerifyOptions{})
	if !errors.Is(err, darwinbundle.ErrCodesignUnavailable) {
		t.Errorf("Verify: got %v, want ErrCodesignUnavailable", err)
	}
	_, err = darwinbundle.Inspect(context.Background(), "x")
	if !errors.Is(err, darwinbundle.ErrCodesignUnavailable) {
		t.Errorf("Inspect: got %v, want ErrCodesignUnavailable", err)
	}
	if err != nil && !strings.Contains(err.Error(), "ships with macOS") {
		t.Errorf("the error does not explain itself: %v", err)
	}
}

func TestVerifyAcceptsWhatCodesignAccepts(t *testing.T) {
	log := fakeCodesign(t, `echo "valid on disk" >&2; exit 0`)
	res, err := darwinbundle.Verify(context.Background(), "/some/App.app", darwinbundle.VerifyOptions{})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.Valid || res.Path != "/some/App.app" || !strings.Contains(res.Output, "valid on disk") {
		t.Errorf("result = %+v", res)
	}
	if got := readFile(t, log); !strings.Contains(got, "--verify --verbose=4 /some/App.app") {
		t.Errorf("codesign was called as: %s", got)
	}
}

func TestVerifyRefusesWhatCodesignRefuses(t *testing.T) {
	fakeCodesign(t, `echo "a sealed resource is missing or invalid" >&2; exit 3`)
	res, err := darwinbundle.Verify(context.Background(), "/some/App.app", darwinbundle.VerifyOptions{Deep: true})
	if err == nil {
		t.Fatal("a rejected signature was accepted")
	}
	if res.Valid {
		t.Error("the result says Valid")
	}
	// The explanation is in the error and in the result.
	for _, got := range []string{err.Error(), res.Output} {
		if !strings.Contains(got, "sealed resource is missing") {
			t.Errorf("codesign's explanation is missing from %q", got)
		}
	}
}

func TestDeepAddsDeepAndStrict(t *testing.T) {
	log := fakeCodesign(t, `exit 0`)
	if _, err := darwinbundle.Verify(context.Background(), "p", darwinbundle.VerifyOptions{Deep: true}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, log); !strings.Contains(got, "--deep --strict") {
		t.Errorf("codesign was called as: %s", got)
	}
	log2 := fakeCodesign(t, `exit 0`)
	if _, err := darwinbundle.Verify(context.Background(), "p", darwinbundle.VerifyOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, log2); strings.Contains(got, "--deep") {
		t.Errorf("a shallow verify was deep: %s", got)
	}
}

func TestVerifyReportsEntitlementsWhenThereAreSome(t *testing.T) {
	fakeCodesign(t, `
case "$*" in
  *--entitlements*) echo '<plist><dict><key>k</key><true/></dict></plist>'; exit 0 ;;
esac
exit 0`)
	res, err := darwinbundle.Verify(context.Background(), "p", darwinbundle.VerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Entitlements, "<key>k</key>") {
		t.Errorf("Entitlements = %q", res.Entitlements)
	}
}

// Failing to read entitlements says nothing about whether the signature is
// valid, so it must not turn a good result into an error.
func TestMissingEntitlementsDoNotFailVerification(t *testing.T) {
	fakeCodesign(t, `
case "$*" in
  *--entitlements*) echo "no entitlements" >&2; exit 1 ;;
esac
exit 0`)
	res, err := darwinbundle.Verify(context.Background(), "p", darwinbundle.VerifyOptions{})
	if err != nil || !res.Valid || res.Entitlements != "" {
		t.Errorf("got %+v, %v", res, err)
	}
}

func TestInspectTreatsUnsignedAsAnAnswerNotAFailure(t *testing.T) {
	fakeCodesign(t, `echo "/some/file: code object is not signed at all" >&2; exit 1`)
	sig, err := darwinbundle.Inspect(context.Background(), "/some/file")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if sig.Signed {
		t.Errorf("an unsigned file was reported signed: %+v", sig)
	}
}

func TestInspectReportsOtherFailures(t *testing.T) {
	fakeCodesign(t, `echo "/some/file: No such file or directory" >&2; exit 1`)
	_, err := darwinbundle.Inspect(context.Background(), "/some/file")
	if err == nil || !strings.Contains(err.Error(), "No such file") {
		t.Errorf("got %v", err)
	}
}

func TestInspectReadsTheCannedOutput(t *testing.T) {
	fakeCodesign(t, `cat '`+filepath.Join(mustAbs(t, "testdata/codesign"), "team.txt")+`' >&2; exit 0`)
	sig, err := darwinbundle.Inspect(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	if !sig.Signed || sig.TeamID != "TEAM123456" || sig.AdHoc || !sig.HardenedRuntime {
		t.Errorf("got %+v", sig)
	}
}

// A cancelled context must end the wait. The script replaces itself with sleep
// so that killing the process kills the sleep too; otherwise the pipe stays open
// and the wait outlasts the cancellation, which would be a defect of the test
// rather than of the code.
func TestVerifyStopsWhenTheContextIsCancelled(t *testing.T) {
	fakeCodesign(t, `exec sleep 30`)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := darwinbundle.Verify(ctx, "p", darwinbundle.VerifyOptions{})
	if err == nil {
		t.Fatal("no error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got %v, want the deadline", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("took %v to give up", took)
	}
}

func TestInspectStopsWhenTheContextIsCancelled(t *testing.T) {
	fakeCodesign(t, `exec sleep 30`)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := darwinbundle.Inspect(ctx, "p"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got %v, want the deadline", err)
	}
}

// ---- against the real tool ----

func realCodesign(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("the real codesign is macOS-only")
	}
	if _, err := exec.LookPath("codesign"); err != nil {
		t.Skip("codesign is not installed")
	}
}

func TestInspectReadsRealSignatures(t *testing.T) {
	realCodesign(t)
	ctx := context.Background()

	adhoc := copyOf(t, thinBinary(t, "arm64"))
	if err := darwinbundle.Sign(adhoc, darwinbundle.SignConfig{AdHoc: true}); err != nil {
		t.Fatal(err)
	}
	if sig, err := darwinbundle.Inspect(ctx, adhoc); err != nil || !sig.Signed || !sig.AdHoc || sig.TeamID != "" {
		t.Errorf("ad hoc: %+v, %v", sig, err)
	}

	team := copyOf(t, thinBinary(t, "arm64"))
	if err := darwinbundle.Sign(team, darwinbundle.SignConfig{Signer: newIdentity(t).signer(), Identifier: "com.example.real"}); err != nil {
		t.Fatal(err)
	}
	sig, err := darwinbundle.Inspect(ctx, team)
	if err != nil {
		t.Fatal(err)
	}
	if !sig.Signed || sig.AdHoc || sig.TeamID != testTeam || sig.Identifier != "com.example.real" || !sig.HardenedRuntime {
		t.Errorf("team: %+v", sig)
	}

	unsigned := copyOf(t, thinBinary(t, "arm64"))
	if o, err := exec.Command("codesign", "--remove-signature", unsigned).CombinedOutput(); err != nil {
		t.Fatalf("%s", o)
	}
	if sig, err := darwinbundle.Inspect(ctx, unsigned); err != nil || sig.Signed {
		t.Errorf("unsigned: %+v, %v", sig, err)
	}
}

func TestVerifyAgainstTheRealTool(t *testing.T) {
	realCodesign(t)
	bin := copyOf(t, thinBinary(t, "arm64"))
	if err := darwinbundle.Sign(bin, darwinbundle.SignConfig{Signer: newIdentity(t).signer()}); err != nil {
		t.Fatal(err)
	}
	res, err := darwinbundle.Verify(context.Background(), bin, darwinbundle.VerifyOptions{})
	if err != nil || !res.Valid {
		t.Fatalf("a freshly signed binary was rejected: %+v, %v", res, err)
	}

	// Change the file after signing; the seal must notice.
	// The byte is inverted rather than set to a constant, which would change
	// nothing if it happened to hold that value already.
	f, err := os.OpenFile(bin, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 1)
	if _, err := f.ReadAt(b, 4096); err != nil {
		t.Fatal(err)
	}
	b[0] ^= 0xFF
	if _, err := f.WriteAt(b, 4096); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if res, err := darwinbundle.Verify(context.Background(), bin, darwinbundle.VerifyOptions{}); err == nil || res.Valid {
		t.Errorf("a modified binary still verifies: %+v", res)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}
