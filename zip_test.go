package macbundle_test

import (
	"archive/zip"
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/TotallyGamerJet/macbundle"
)

// fixtureBundle builds a tree with the shapes that matter: a nested directory,
// an executable, a plain file, a directory with nothing in it, and a symlink.
func fixtureBundle(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "Example.app")

	for _, dir := range []string{
		filepath.Join(root, "Contents", "MacOS"),
		filepath.Join(root, "Contents", "Resources"), // stays empty on purpose
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "Contents", "MacOS", "Example"),
		[]byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Contents", "Info.plist"),
		[]byte("<plist/>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("MacOS/Example", filepath.Join(root, "Contents", "Current")); err != nil {
		t.Fatal(err)
	}
	return root
}

// openZip reads an archive back as a map from entry name to entry.
func openZip(t *testing.T, path string) (map[string]*zip.File, func()) {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	files := make(map[string]*zip.File, len(r.File))
	for _, f := range r.File {
		files[f.Name] = f
	}
	return files, func() {
		if err := r.Close(); err != nil {
			t.Error("closing the archive:", err)
		}
	}
}

func readZipEntry(t *testing.T, f *zip.File) string {
	t.Helper()
	rc, err := f.Open()
	if err != nil {
		t.Fatalf("opening the entry %s: %v", f.Name, err)
	}
	defer func() {
		if err := rc.Close(); err != nil {
			t.Error("closing the entry:", err)
		}
	}()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading the entry %s: %v", f.Name, err)
	}
	return string(b)
}

// The parent directory's name has to lead every entry, or unpacking spills
// Contents/ into whatever directory the archive was opened in. This is what
// `ditto --keepParent` was there for.
func TestZipKeepsTheParentDirectory(t *testing.T) {
	root := fixtureBundle(t)
	out := filepath.Join(t.TempDir(), "Example.zip")
	if err := macbundle.Zip(out, root); err != nil {
		t.Fatal(err)
	}

	files, done := openZip(t, out)
	defer done()

	for name := range files {
		if !strings.HasPrefix(name, "Example.app/") && name != "Example.app/" {
			t.Errorf("entry %q is not under the bundle directory", name)
		}
	}
	for _, want := range []string{
		"Example.app/",
		"Example.app/Contents/",
		"Example.app/Contents/Info.plist",
		"Example.app/Contents/MacOS/",
		"Example.app/Contents/MacOS/Example",
	} {
		if _, ok := files[want]; !ok {
			t.Errorf("missing entry %q; archive holds %v", want, slices.Sorted(maps.Keys(files)))
		}
	}
	if got := readZipEntry(t, files["Example.app/Contents/Info.plist"]); got != "<plist/>\n" {
		t.Errorf("Info.plist round-tripped as %q", got)
	}
}

// The executable bit is the whole reason ditto was used rather than a plain
// zip: a bundle whose binary comes back 0644 does not launch, and nothing about
// the archive looks wrong.
func TestZipPreservesTheExecutableBit(t *testing.T) {
	root := fixtureBundle(t)
	out := filepath.Join(t.TempDir(), "Example.zip")
	if err := macbundle.Zip(out, root); err != nil {
		t.Fatal(err)
	}

	files, done := openZip(t, out)
	defer done()

	if got := files["Example.app/Contents/MacOS/Example"].Mode().Perm(); got != 0o755 {
		t.Errorf("executable stored as %04o, want 0755", got)
	}
	if got := files["Example.app/Contents/Info.plist"].Mode().Perm(); got != 0o644 {
		t.Errorf("plist stored as %04o, want 0644", got)
	}
	if got := files["Example.app/Contents/MacOS/"].Mode(); !got.IsDir() {
		t.Errorf("directory entry stored with mode %v, want a directory", got)
	}
}

// A symlink must survive as a symlink. Stored as a regular file it becomes a
// copy — or, in a framework, a duplicate of everything under Versions.
func TestZipPreservesSymlinks(t *testing.T) {
	root := fixtureBundle(t)
	out := filepath.Join(t.TempDir(), "Example.zip")
	if err := macbundle.Zip(out, root); err != nil {
		t.Fatal(err)
	}

	files, done := openZip(t, out)
	defer done()

	link, ok := files["Example.app/Contents/Current"]
	if !ok {
		t.Fatal("the symlink is missing from the archive")
	}
	if link.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("the symlink is stored with mode %v, want a symlink", link.Mode())
	}
	if got := readZipEntry(t, link); got != "MacOS/Example" {
		t.Errorf("link target stored as %q, want %q", got, "MacOS/Example")
	}
}

// An empty directory has no entries to imply it, so it survives only if it is
// written explicitly. Contents/Resources is the usual example.
func TestZipRecordsEmptyDirectories(t *testing.T) {
	root := fixtureBundle(t)
	out := filepath.Join(t.TempDir(), "Example.zip")
	if err := macbundle.Zip(out, root); err != nil {
		t.Fatal(err)
	}

	files, done := openZip(t, out)
	defer done()

	if _, ok := files["Example.app/Contents/Resources/"]; !ok {
		t.Errorf("the empty directory is missing; archive holds %v", slices.Sorted(maps.Keys(files)))
	}
}

func TestZipRejectsANonDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "notabundle")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := macbundle.Zip(filepath.Join(t.TempDir(), "out.zip"), file); err == nil {
		t.Error("archiving a regular file succeeded")
	}

	missing := filepath.Join(t.TempDir(), "nothing.app")
	err := macbundle.Zip(filepath.Join(t.TempDir(), "out.zip"), missing)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("archiving a missing path gave %v, want a not-exist error", err)
	}
}

// Writing the archive into the tree it archives would walk it into itself.
func TestZipRefusesAnArchiveInsideTheTree(t *testing.T) {
	root := fixtureBundle(t)
	for _, out := range []string{
		filepath.Join(root, "inside.zip"),
		filepath.Join(root, "Contents", "deep", "inside.zip"),
	} {
		if err := macbundle.Zip(out, root); err == nil {
			t.Errorf("an archive at %s was accepted", out)
		}
		if _, err := os.Stat(out); err == nil {
			t.Errorf("%s was created", out)
		}
	}
	// A sibling is fine, including one whose name merely starts with the tree's.
	sibling := root + ".zip"
	if err := macbundle.Zip(sibling, root); err != nil {
		t.Errorf("a sibling archive was refused: %v", err)
	}
}

func TestZipCreatesTheOutputDirectory(t *testing.T) {
	out := filepath.Join(t.TempDir(), "a", "b", "Example.zip")
	if err := macbundle.Zip(out, fixtureBundle(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Error(err)
	}
}
