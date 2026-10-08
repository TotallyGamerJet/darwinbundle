//go:build unix

package darwinbundle_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/TotallyGamerJet/darwinbundle"
)

// Anything that is not a file, directory or symlink is refused. Skipping it
// would produce an archive that is quietly not the tree it claims to be.
func TestZipRefusesAnIrregularFile(t *testing.T) {
	root := fixtureBundle(t)
	fifo := filepath.Join(root, "Contents", "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("cannot create a fifo here: %v", err)
	}

	err := darwinbundle.Zip(filepath.Join(t.TempDir(), "Example.zip"), root)
	if err == nil {
		t.Fatal("archiving a bundle containing a fifo succeeded")
	}
	if !strings.Contains(err.Error(), "pipe") {
		t.Errorf("error does not name the offending path: %v", err)
	}
}

// A failure part-way through leaves a truncated zip, which unpacks to a bundle
// missing files and looks like any other archive. It is removed instead.
func TestAFailedZipLeavesNoArchiveBehind(t *testing.T) {
	root := fixtureBundle(t)
	if err := syscall.Mkfifo(filepath.Join(root, "Contents", "pipe"), 0o644); err != nil {
		t.Skipf("cannot create a fifo here: %v", err)
	}
	out := filepath.Join(t.TempDir(), "Example.zip")
	if err := darwinbundle.Zip(out, root); err == nil {
		t.Fatal("expected a failure")
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("a partial archive was left behind")
	}
}

// The archive is read by the platform's own unzip, not only by archive/zip,
// which would be agreeing with itself: the executable bit and the symlink have
// to come out the other side.
func TestUnzipRestoresModesAndLinks(t *testing.T) {
	unzip, err := exec.LookPath("unzip")
	if err != nil {
		t.Skip("unzip is not installed")
	}
	root := fixtureBundle(t)
	out := filepath.Join(t.TempDir(), "Example.zip")
	if err := darwinbundle.Zip(out, root); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	if o, err := exec.Command(unzip, "-q", out, "-d", dest).CombinedOutput(); err != nil {
		t.Fatalf("unzip: %v\n%s", err, o)
	}

	bin := filepath.Join(dest, "Example.app", "Contents", "MacOS", "Example")
	if fi, err := os.Stat(bin); err != nil || fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("the executable came out as %v, %v", fi, err)
	}
	link := filepath.Join(dest, "Example.app", "Contents", "Current")
	if target, err := os.Readlink(link); err != nil || target != "MacOS/Example" {
		t.Errorf("the symlink came out as %q, %v", target, err)
	}
	if fi, err := os.Stat(filepath.Join(dest, "Example.app", "Contents", "Resources")); err != nil || !fi.IsDir() {
		t.Errorf("the empty directory did not survive: %v", err)
	}
}
