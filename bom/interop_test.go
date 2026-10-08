package bom_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/TotallyGamerJet/darwinbundle/bom"
)

// Reading files this package did not write is the only evidence that the format
// here is Apple's rather than merely self-consistent. A round trip through our
// own writer and reader would pass just as well with the fields in the wrong
// order.
//
// These skip anywhere the files are not present, which is every machine that is
// not a Mac — so CI on Linux runs the round-trip tests and quietly skips these.

func appleBOMs(t *testing.T) []string {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("Apple's own BOM files exist only on macOS")
	}
	found, err := filepath.Glob("/var/db/receipts/*.bom")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) == 0 {
		t.Skip("no receipts in /var/db/receipts to read")
	}
	if len(found) > 12 {
		found = found[:12]
	}
	return found
}

// TestReadsApplesOwnReceipts.
//
// A receipt is a BOM macOS wrote itself, and it exercises the parts a small
// hand-made file does not: block tables with thousands of unused entries, trees
// deep enough to have internal nodes, and leaves linked across pages.
//
// The count is the assertion that matters. A tree's header records how many
// entries it holds, and this walks the leaves and compares — so a mistake in
// the leaf links, the node header width, or the pair order shows up as a short
// walk rather than as something that merely looks plausible.
func TestReadsApplesOwnReceipts(t *testing.T) {
	for _, path := range appleBOMs(t) {
		data, err := os.ReadFile(path)
		if err != nil {
			// Some receipts are readable only by root; that is not a failure of
			// this package.
			t.Logf("skipping %s: %v", filepath.Base(path), err)
			continue
		}
		r, err := bom.Parse(data)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(path), err)
			continue
		}

		vars := r.Variables()
		if len(vars) == 0 {
			t.Errorf("%s: no variables", filepath.Base(path))
			continue
		}
		// Every receipt Apple writes has these.
		for _, want := range []string{"BomInfo", "Paths"} {
			if !slices.Contains(vars, want) {
				t.Errorf("%s: variables are %v, want one named %s",
					filepath.Base(path), vars, want)
			}
		}

		// Tree itself checks the walked count against the header's, so
		// reaching here without an error is the assertion.
		paths, err := r.Tree("Paths")
		if err != nil {
			t.Errorf("%s: reading the Paths tree: %v", filepath.Base(path), err)
			continue
		}
		t.Logf("%s: %d blocks, %d paths", filepath.Base(path), r.NumBlocks(), len(paths))
	}
}

// TestAgreesWithLsbom checks the path count against Apple's own reader rather
// than against the file's own header, so a file that is internally consistent
// but misunderstood still fails.
func TestAgreesWithLsbom(t *testing.T) {
	lsbom, err := exec.LookPath("lsbom")
	if err != nil {
		t.Skip("lsbom is not installed")
	}

	for _, path := range appleBOMs(t) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		// -s prints one path per line. Counting whitespace-separated fields
		// instead would over-count every path containing a space, which
		// receipts for third-party packages are full of.
		out, err := exec.Command(lsbom, "-s", path).Output()
		if err != nil {
			continue
		}
		trimmed := strings.TrimRight(string(out), "\n")
		if trimmed == "" {
			continue
		}
		want := len(strings.Split(trimmed, "\n"))

		r, err := bom.Parse(data)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(path), err)
			continue
		}
		got, err := r.Tree("Paths")
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(path), err)
			continue
		}
		if len(got) != want {
			t.Errorf("%s: walked %d paths, lsbom lists %d",
				filepath.Base(path), len(got), want)
		}
	}
}

// TestReadsACompiledAssetCatalog.
//
// The format this package exists for. A .car is a BOM whose variables are
// CoreUI structures, so parsing one proves the container is understood before
// anything tries to build one — and the variable names here are the list
// package assetcatalog has to produce.
func TestReadsACompiledAssetCatalog(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("system asset catalogs exist only on macOS")
	}
	matches, err := filepath.Glob(
		"/System/Library/PrivateFrameworks/*.framework/Versions/A/Resources/Assets.car")
	if err != nil {
		t.Fatal(err)
	}
	var read int
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		r, err := bom.Parse(data)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		vars := r.Variables()
		for _, want := range []string{"CARHEADER", "KEYFORMAT", "RENDITIONS", "FACETKEYS"} {
			if !slices.Contains(vars, want) {
				t.Errorf("%s: variables are %v, want one named %s", path, vars, want)
			}
		}
		// The two trees a catalog is really made of.
		for _, name := range []string{"RENDITIONS", "FACETKEYS"} {
			if _, err := r.Tree(name); err != nil {
				t.Errorf("%s: reading %s: %v", path, name, err)
			}
		}
		read++
		if read == 5 {
			break
		}
	}
	if read == 0 {
		t.Skip("no readable system asset catalogs")
	}
	t.Logf("read %d system asset catalogs", read)
}

// TestApplesReaderAcceptsOurWriter.
//
// The other direction, and the one that matters for a package whose job is to
// produce files. Round-tripping through our own reader proves only that the two
// halves agree with each other; this takes a receipt macOS wrote, pulls it
// apart, rebuilds it with our writer, and has lsbom read the result.
//
// Block numbers are preserved slot for slot, including the null gaps Apple's
// over-allocated table leaves behind, because a tree's entries are slot
// numbers: renumbering the blocks would silently rewire the tree.
func TestApplesReaderAcceptsOurWriter(t *testing.T) {
	lsbom, err := exec.LookPath("lsbom")
	if err != nil {
		t.Skip("lsbom is not installed")
	}

	var checked int
	for _, path := range appleBOMs(t) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		original, err := exec.Command(lsbom, path).Output()
		if err != nil {
			continue
		}

		r, err := bom.Parse(data)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(path), err)
			continue
		}

		w := bom.NewWriter()
		for slot := 1; slot < r.TableLen(); slot++ {
			b, err := r.Block(bom.BlockID(slot))
			if err != nil {
				t.Fatalf("%s: block %d: %v", filepath.Base(path), slot, err)
			}
			if got := w.AddBlock(b); got != bom.BlockID(slot) {
				t.Fatalf("%s: rebuilding put block %d in slot %d", filepath.Base(path), slot, got)
			}
		}
		for _, name := range r.Variables() {
			id, err := r.Variable(name)
			if err != nil {
				t.Fatal(err)
			}
			if err := w.SetVariable(name, id); err != nil {
				t.Fatal(err)
			}
		}

		rebuilt, err := w.Bytes()
		if err != nil {
			t.Fatalf("%s: %v", filepath.Base(path), err)
		}
		out := filepath.Join(t.TempDir(), "rebuilt.bom")
		if err := os.WriteFile(out, rebuilt, 0o644); err != nil {
			t.Fatal(err)
		}

		got, err := exec.Command(lsbom, out).Output()
		if err != nil {
			t.Errorf("%s: lsbom refused our rebuilt file: %v", filepath.Base(path), err)
			continue
		}
		if string(got) != string(original) {
			t.Errorf("%s: lsbom reads our rebuilt file differently from the original\n"+
				"original %d bytes of output, ours %d",
				filepath.Base(path), len(original), len(got))
			continue
		}
		checked++
	}

	if checked == 0 {
		t.Skip("no receipt could be transcoded")
	}
	t.Logf("lsbom read %d rebuilt receipts identically to the originals", checked)
}
