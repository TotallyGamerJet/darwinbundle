package assetcatalog_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/TotallyGamerJet/macbundle/assetcatalog"
	"github.com/TotallyGamerJet/macbundle/bom"
)

func build(t *testing.T, c *assetcatalog.Catalog) []byte {
	t.Helper()
	data, err := c.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func sample() *assetcatalog.Catalog {
	c := &assetcatalog.Catalog{Creator: "macbundle test"}
	c.AddFacet(assetcatalog.Facet{
		Name: "AppIcon",
		Key: assetcatalog.Key{
			assetcatalog.AttrElement:    85,
			assetcatalog.AttrPart:       220,
			assetcatalog.AttrIdentifier: 6849,
		},
	})
	return c
}

// The variables CoreUI looks for have to be present and have to be the right
// shape, which the container itself has no opinion about.
func TestCatalogHasTheVariablesCoreUIExpects(t *testing.T) {
	r, err := bom.Parse(build(t, sample()))
	if err != nil {
		t.Fatalf("parsing what we wrote: %v", err)
	}

	for _, name := range []string{"CARHEADER", "KEYFORMAT", "FACETKEYS", "RENDITIONS"} {
		if _, err := r.Variable(name); err != nil {
			t.Errorf("the catalog has no %s: %v", name, err)
		}
	}

	head, err := r.VariableBlock("CARHEADER")
	if err != nil {
		t.Fatal(err)
	}
	if len(head) != 436 {
		t.Errorf("CARHEADER is %d bytes, want 436", len(head))
	}
	// Little-endian inside a big-endian container: 'CTAR' reads as RATC.
	if string(head[:4]) != "RATC" {
		t.Errorf("CARHEADER starts %q, want RATC", head[:4])
	}

	kf, err := r.VariableBlock("KEYFORMAT")
	if err != nil {
		t.Fatal(err)
	}
	if want := 12 + 4*len(assetcatalog.DefaultKeyFormat()); len(kf) != want {
		t.Errorf("KEYFORMAT is %d bytes, want %d", len(kf), want)
	}
	if string(kf[:4]) != "tmfk" {
		t.Errorf("KEYFORMAT starts %q, want tmfk", kf[:4])
	}
}

// A facet's value encodes the same bytes whichever order Go happens to walk the
// key map in. Without a fixed order the catalog would differ between builds.
func TestFacetEncodingIsDeterministic(t *testing.T) {
	first := build(t, sample())
	for range 20 {
		if got := build(t, sample()); string(got) != string(first) {
			t.Fatal("two builds of the same catalog produced different bytes")
		}
	}
}

func TestWriteToWritesWhatBytesReturns(t *testing.T) {
	var buf bytes.Buffer
	n, err := sample().WriteTo(&buf)
	if err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	want := build(t, sample())
	if !bytes.Equal(buf.Bytes(), want) {
		t.Error("WriteTo and Bytes produced different files")
	}
	if n != int64(len(want)) {
		t.Errorf("WriteTo reported %d bytes, wrote %d", n, len(want))
	}
}

// The key format is what gives a rendition key its meaning, so the default is
// handed out as a copy. A caller that edits what it was given must not change
// what every later catalog is written with.
func TestDefaultKeyFormatCannotBeEditedInPlace(t *testing.T) {
	first := assetcatalog.DefaultKeyFormat()
	want := append([]assetcatalog.Attribute(nil), first...)
	for i := range first {
		first[i] = 0
	}
	if got := assetcatalog.DefaultKeyFormat(); !slices.Equal(got, want) {
		t.Errorf("editing the returned slice changed the default: got %v, want %v", got, want)
	}
	// And the catalog still writes the original order.
	before := build(t, sample())
	if after := build(t, sample()); !bytes.Equal(before, after) {
		t.Error("a catalog changed after the returned default was edited")
	}
}

func TestAFacetNeedsAName(t *testing.T) {
	c := &assetcatalog.Catalog{}
	c.AddFacet(assetcatalog.Facet{Key: assetcatalog.Key{assetcatalog.AttrPart: 1}})
	if _, err := c.Bytes(); err == nil {
		t.Error("a facet with no name was accepted")
	}
}

// TestAssetutilReadsWhatWeWrote.
//
// The only test here that is worth much. Everything else checks our writer
// against our own reader; this hands the file to Apple's, which is the one that
// decides whether a catalog is a catalog.
//
// It is deliberately not asserting anything about renditions: this package does
// not encode image data yet, so what is being checked is that the container,
// the header, the key format and the facet tree are all well enough formed for
// CoreUI to parse the catalog at all.
func TestAssetutilReadsWhatWeWrote(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("assetutil is macOS-only")
	}
	assetutil, err := exec.LookPath("assetutil")
	if err != nil {
		t.Skip("assetutil is not installed")
	}

	path := filepath.Join(t.TempDir(), "Assets.car")
	if err := os.WriteFile(path, build(t, sample()), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(assetutil, "-I", path).CombinedOutput()
	if err != nil {
		t.Fatalf("assetutil refused our catalog: %v\n%s", err, out)
	}

	var entries []map[string]any
	if err := json.Unmarshal(out, &entries); err != nil {
		t.Fatalf("assetutil printed something we cannot parse: %v\n%s", err, out)
	}
	if len(entries) == 0 {
		t.Fatal("assetutil read the catalog as empty")
	}

	header := entries[0]
	if got, ok := header["CoreUIVersion"].(float64); !ok || uint32(got) != assetcatalog.CoreUIVersion {
		t.Errorf("assetutil reports CoreUIVersion %v, want %d", header["CoreUIVersion"], assetcatalog.CoreUIVersion)
	}
	if got, ok := header["StorageVersion"].(float64); !ok || uint32(got) != assetcatalog.StorageVersion {
		t.Errorf("assetutil reports StorageVersion %v, want %d", header["StorageVersion"], assetcatalog.StorageVersion)
	}
	// The key format is what makes a rendition key mean anything, so it is
	// worth checking Apple's reader agrees with the order we wrote.
	format, ok := header["Key Format"].([]any)
	if !ok {
		t.Fatalf("assetutil reports no key format: %v", header)
	}
	if len(format) != len(assetcatalog.DefaultKeyFormat()) {
		t.Errorf("assetutil reports %d key-format entries, want %d",
			len(format), len(assetcatalog.DefaultKeyFormat()))
	}
	if len(format) > 0 && format[0] != "kCRThemeAppearanceName" {
		t.Errorf("the key format starts with %v, want kCRThemeAppearanceName", format[0])
	}
	t.Logf("assetutil read our catalog: %v", header["Key Format"])
}
