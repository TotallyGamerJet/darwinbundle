package assetcatalog_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/TotallyGamerJet/darwinbundle/assetcatalog"
	"github.com/TotallyGamerJet/darwinbundle/bom"
)

// testImage is deterministic so that a byte comparison against actool's output
// means something. Opaque, because a premultiplied format hides mistakes in
// transparent pixels.
func testImage(n int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	for y := range n {
		for x := range n {
			img.Set(x, y, color.NRGBA{
				R: uint8(x * 255 / n), G: 0x50, B: uint8(y * 255 / n), A: 0xFF,
			})
		}
	}
	return img
}

func writePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// renditionCSI pulls the one bitmap rendition out of a catalog.
func renditionCSIs(t *testing.T, data []byte) [][]byte {
	t.Helper()
	r, err := bom.Parse(data)
	if err != nil {
		t.Fatalf("parsing the catalog: %v", err)
	}
	entries, err := r.Tree("RENDITIONS")
	if err != nil {
		t.Fatalf("reading RENDITIONS: %v", err)
	}
	var out [][]byte
	for _, e := range entries {
		// The catalog actool writes also holds a rendition with no image in
		// it, which carries the icon's name rather than any pixels.
		if len(e.Value) > 300 {
			out = append(out, e.Value)
		}
	}
	return out
}

// TestOurRenditionMatchesActool.
//
// The test this format needed. Everything else here compares our writer against
// our own reader, which would pass just as happily with the info list in the
// wrong place; this compiles the same PNG with actool and compares the CSI blob
// byte for byte.
//
// A mismatch prints the offset, which is what made the format tractable at all:
// the difference is always a field, and the offset says which.
func TestOurRenditionMatchesActool(t *testing.T) {
	// More than one size, deliberately. A 16-pixel image hides any field that
	// happens to equal 64 at that width — the stride did, and passed a byte
	// comparison while making CoreUI refuse the image.
	for _, size := range []int{16, 32, 128} {
		t.Run(fmt.Sprintf("%dx%d", size, size), func(t *testing.T) {
			compareWithActool(t, size)
		})
	}
}

func compareWithActool(t *testing.T, size int) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("actool is macOS-only")
	}
	actool, err := exec.LookPath("actool")
	if err != nil {
		t.Skip("actool is not installed")
	}

	dir := t.TempDir()
	set := filepath.Join(dir, "AppIcon.appiconset")
	if err := os.MkdirAll(set, 0o755); err != nil {
		t.Fatal(err)
	}
	img := testImage(size)
	name := fmt.Sprintf("icon_%d.png", size)
	writePNG(t, filepath.Join(set, name), img)
	contents := fmt.Sprintf(`{"images":[{"filename":%q,"idiom":"mac",`+
		`"scale":"1x","size":"%dx%d"}],"info":{"author":"xcode","version":1}}`, name, size, size)
	if err := os.WriteFile(filepath.Join(set, "Contents.json"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(actool, "--compile", out, "--platform", "macosx",
		"--minimum-deployment-target", "13.0", "--app-icon", "AppIcon",
		"--output-partial-info-plist", filepath.Join(dir, "partial.plist"), dir)
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("actool could not compile the fixture: %v\n%s", err, combined)
	}
	reference, err := os.ReadFile(filepath.Join(out, "Assets.car"))
	if err != nil {
		t.Skipf("actool produced no catalog: %v", err)
	}

	want := renditionCSIs(t, reference)
	if len(want) != 1 {
		t.Fatalf("the reference holds %d bitmap renditions, want 1", len(want))
	}

	c := &assetcatalog.Catalog{Creator: "darwinbundle test"}
	c.AddRendition(assetcatalog.Rendition{
		Name:  name,
		Image: img,
		Scale: 1,
		Key: assetcatalog.Key{
			assetcatalog.AttrElement:    85,
			assetcatalog.AttrPart:       220,
			assetcatalog.AttrIdentifier: 6849,
			assetcatalog.AttrDimension2: 1,
			assetcatalog.AttrScale:      1,
		},
	})
	data, err := c.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	got := renditionCSIs(t, data)
	if len(got) != 1 {
		t.Fatalf("we wrote %d bitmap renditions, want 1", len(got))
	}

	// Everything up to the payload length is compared for every size. actool
	// compresses images above 16 pixels and this package does not, so the
	// lengths and the bytes after them legitimately differ — but every field
	// describing the image must still agree exactly.
	//
	// The header runs to offset 180: the CSI fields, the name, the info list's
	// length, and the first two words of the info list. Offset 180 is the size
	// of the payload, which is where a compression choice shows up.
	const headerEnd = 180
	if len(got[0]) < headerEnd || len(want[0]) < headerEnd {
		t.Fatalf("a rendition is too short to compare: ours %d, actool's %d",
			len(got[0]), len(want[0]))
	}
	if !bytes.Equal(got[0][:headerEnd], want[0][:headerEnd]) {
		for i := range headerEnd {
			if got[0][i] != want[0][i] {
				lo := max(i-8, 0)
				t.Fatalf("first difference at offset %d (%#x):\n ours %x\nactool %x",
					i, i, got[0][lo:min(lo+32, headerEnd)], want[0][lo:min(lo+32, headerEnd)])
			}
		}
	}

	// The entries after the payload size describe the image rather than its
	// encoding, so those must match too: the slice rectangle, the metrics, and
	// the stride. Comparing them by hand is what caught a hard-coded stride
	// that happened to be right at 16 pixels and wrong everywhere else.
	if !bytes.Equal(infoEntries(t, got[0]), infoEntries(t, want[0])) {
		t.Errorf("our info list describes the image differently from actool's:\n ours %x\nactool %x",
			infoEntries(t, got[0]), infoEntries(t, want[0]))
	}

	// Where the two happened to make the same storage choice, nothing may
	// differ at all. They often do not: actool uses LZFSE above sixteen pixels
	// and this package uses zlib whenever zlib is smaller, so the comparison
	// that carries the weight is the one above.
	if compressionOf(t, want[0]) == compressionOf(t, got[0]) && !bytes.Equal(got[0], want[0]) {
		t.Errorf("both stored this size the same way and the bytes still differ: "+
			"ours %d, actool's %d", len(got[0]), len(want[0]))
	}
}

// infoEntries returns the info list's TLV entries, which describe the image.
// The 12-byte prefix is skipped: its third word is the payload size, and that
// depends on the compression rather than on the image.
func infoEntries(t *testing.T, csi []byte) []byte {
	t.Helper()
	const infoLenOffset, prefix = 168, 12
	n := int(binary.LittleEndian.Uint32(csi[infoLenOffset:]))
	start := infoLenOffset + 4 + prefix
	if start+n-prefix > len(csi) {
		t.Fatalf("the info list runs past the end of a %d-byte rendition", len(csi))
	}
	return csi[start : start+n-prefix]
}

// compressionOf reads the CELM block's compression field.
func compressionOf(t *testing.T, csi []byte) uint32 {
	t.Helper()
	i := bytes.LastIndex(csi, []byte("MLEC"))
	if i < 0 {
		t.Fatal("a rendition has no CELM payload")
	}
	return binary.LittleEndian.Uint32(csi[i+8:])
}

// TestAssetutilReadsOurRendition checks the whole catalog through Apple's
// reader, which is what decides whether an icon appears.
func TestAssetutilReadsOurRendition(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("assetutil is macOS-only")
	}
	assetutil, err := exec.LookPath("assetutil")
	if err != nil {
		t.Skip("assetutil is not installed")
	}

	c := &assetcatalog.Catalog{Creator: "darwinbundle test"}
	c.AddFacet(assetcatalog.Facet{
		Name: "AppIcon",
		Key: assetcatalog.Key{
			assetcatalog.AttrElement:    85,
			assetcatalog.AttrPart:       220,
			assetcatalog.AttrIdentifier: 6849,
		},
	})
	c.AddRendition(assetcatalog.Rendition{
		Name:  "icon_32.png",
		Image: testImage(32),
		Scale: 1,
		Key: assetcatalog.Key{
			assetcatalog.AttrElement:    85,
			assetcatalog.AttrPart:       220,
			assetcatalog.AttrIdentifier: 6849,
			assetcatalog.AttrDimension2: 1,
			assetcatalog.AttrScale:      1,
		},
	})
	data, err := c.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "Assets.car")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	raw, err := exec.Command(assetutil, "-I", path).CombinedOutput()
	if err != nil {
		t.Fatalf("assetutil refused our catalog: %v\n%s", err, raw)
	}
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("assetutil printed something unparseable: %v\n%s", err, raw)
	}

	var image map[string]any
	for _, e := range entries {
		if e["AssetType"] != nil {
			image = e
			break
		}
	}
	if image == nil {
		t.Fatalf("assetutil found no asset in our catalog:\n%s", raw)
	}
	// Scale is not asserted: assetutil reports it for an "Icon Image" and not
	// for the plain "Image" this key produces, and what is being checked here
	// is that CoreUI could decode the pixels at all.
	for key, want := range map[string]any{
		"PixelWidth":  float64(32),
		"PixelHeight": float64(32),
		"Encoding":    "ARGB",
	} {
		if got := image[key]; got != want {
			t.Errorf("assetutil reports %s = %v, want %v", key, got, want)
		}
	}
	t.Logf("assetutil read our rendition: %v %vx%v",
		image["AssetType"], image["PixelWidth"], image["PixelHeight"])
}
