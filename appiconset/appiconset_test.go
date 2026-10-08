package appiconset_test

import (
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/TotallyGamerJet/macbundle/appiconset"
	"github.com/TotallyGamerJet/macbundle/bom"
)

// square is an opaque gradient n pixels on a side.
func square(n int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	for y := range n {
		for x := range n {
			img.Set(x, y, color.NRGBA{R: uint8(x * 255 / n), G: 0x60, B: uint8(y * 255 / n), A: 0xFF})
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

// writeSet lays out a complete icon set in a temporary directory, exactly as a
// tool producing one would: Contents then the PNGs it names.
func writeSet(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Contents.json"), appiconset.Contents("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, e := range appiconset.Entries() {
		writePNG(t, filepath.Join(dir, e.File), square(e.Pixels()))
	}
	return dir
}

func TestEntriesCoverTheTenSizesMacOSAsksFor(t *testing.T) {
	got := appiconset.Entries()
	if len(got) != 10 {
		t.Fatalf("got %d entries, want 10", len(got))
	}
	// 16, 32, 128, 256 and 512 points, each at 1x and 2x.
	for _, pt := range []int{16, 32, 128, 256, 512} {
		for _, scale := range []int{1, 2} {
			if !slices.ContainsFunc(got, func(e appiconset.Entry) bool {
				return e.Points == pt && e.Scale == scale
			}) {
				t.Errorf("no entry for %dpt at %dx", pt, scale)
			}
		}
	}
	// A 32-pixel image is needed twice, as 32pt@1x and as 16pt@2x.
	px := map[int]int{}
	for _, e := range got {
		px[e.Pixels()]++
	}
	if px[32] != 2 {
		t.Errorf("%d entries need 32 pixels, want 2", px[32])
	}
}

func TestEntriesCannotBeEditedInPlace(t *testing.T) {
	first := appiconset.Entries()
	first[0].File = "tampered.png"
	if appiconset.Entries()[0].File == "tampered.png" {
		t.Error("editing the returned slice changed the list for everyone")
	}
}

func TestContentsIsValidAndNamesEveryEntry(t *testing.T) {
	var c struct {
		Images []struct {
			Filename, Idiom, Scale, Size string
		}
		Info struct {
			Author  string
			Version int
		}
	}
	if err := json.Unmarshal(appiconset.Contents("someone"), &c); err != nil {
		t.Fatalf("Contents is not valid JSON: %v", err)
	}
	if len(c.Images) != len(appiconset.Entries()) {
		t.Fatalf("Contents names %d images, want %d", len(c.Images), len(appiconset.Entries()))
	}
	for i, e := range appiconset.Entries() {
		got := c.Images[i]
		if got.Filename != e.File || got.Idiom != "mac" {
			t.Errorf("image %d: got %+v, want file %s for the mac idiom", i, got, e.File)
		}
		if want := strconv.Itoa(e.Points) + "x" + strconv.Itoa(e.Points); got.Size != want {
			t.Errorf("image %d: size %q, want %q", i, got.Size, want)
		}
		if want := strconv.Itoa(e.Scale) + "x"; got.Scale != want {
			t.Errorf("image %d: scale %q, want %q", i, got.Scale, want)
		}
	}
	if c.Info.Author != "someone" || c.Info.Version != 1 {
		t.Errorf("info is %+v", c.Info)
	}
}

func TestBuildProducesAnIconCatalog(t *testing.T) {
	data, err := appiconset.Build(writeSet(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	r, err := bom.Parse(data)
	if err != nil {
		t.Fatalf("what Build wrote is not a BOM: %v", err)
	}

	facets, err := r.Tree("FACETKEYS")
	if err != nil {
		t.Fatal(err)
	}
	if len(facets) != 1 || string(facets[0].Key) != appiconset.FacetName {
		t.Errorf("facets are %q, want just %q", facets, appiconset.FacetName)
	}

	// Ten images and the one descriptor that tells IconServices the set exists.
	renditions, err := r.Tree("RENDITIONS")
	if err != nil {
		t.Fatal(err)
	}
	if want := len(appiconset.Entries()) + 1; len(renditions) != want {
		t.Errorf("the catalog holds %d renditions, want %d (the images and a descriptor)", len(renditions), want)
	}
}

func TestCompileWritesTheCatalog(t *testing.T) {
	out := filepath.Join(t.TempDir(), "Assets.car")
	if err := appiconset.Compile(writeSet(t), out); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	want, err := appiconset.Build(writeSet(t))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Error("Compile wrote something other than what Build returns")
	}
}

// An entry with no file is a size the designer has not drawn yet. Xcode allows
// it, and it must not stop the rest compiling.
func TestAnEntryWithoutAFileIsSkipped(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "a.png"), square(16))
	writeContents(t, dir, `{"images":[
		{"filename":"a.png","idiom":"mac","scale":"1x","size":"16x16"},
		{"idiom":"mac","scale":"2x","size":"16x16"}]}`)
	if _, err := appiconset.Build(dir); err != nil {
		t.Fatalf("Build: %v", err)
	}
}

func writeContents(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "Contents.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildRefusesWhatCannotBeCompiled(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		files    map[string]image.Image
		want     string // a fragment the error must contain
	}{
		{
			name:     "no Contents.json at all",
			contents: "",
			want:     "reading the icon set",
		},
		{
			name:     "malformed JSON",
			contents: `{"images": [`,
			want:     "not valid JSON",
		},
		{
			name:     "no images",
			contents: `{"images":[]}`,
			want:     "contains no images",
		},
		{
			name:     "every entry undrawn",
			contents: `{"images":[{"idiom":"mac","scale":"1x","size":"16x16"}]}`,
			want:     "contains no images",
		},
		{
			name:     "a size that is not WxH",
			contents: `{"images":[{"filename":"a.png","scale":"1x","size":"sixteen"}]}`,
			want:     "16x16",
		},
		{
			name:     "a scale that is not Nx",
			contents: `{"images":[{"filename":"a.png","scale":"big","size":"16x16"}]}`,
			want:     "2x",
		},
		{
			name:     "a file that is missing",
			contents: `{"images":[{"filename":"gone.png","scale":"1x","size":"16x16"}]}`,
			want:     "reading icon artwork",
		},
		{
			name:     "pixels that do not match the declared size",
			contents: `{"images":[{"filename":"a.png","scale":"2x","size":"16x16"}]}`,
			files:    map[string]image.Image{"a.png": square(16)}, // needs 32
			want:     "pixels wide",
		},
		{
			name:     "an image that is not square",
			contents: `{"images":[{"filename":"a.png","scale":"1x","size":"16x16"}]}`,
			files:    map[string]image.Image{"a.png": image.NewNRGBA(image.Rect(0, 0, 16, 8))},
			want:     "pixels tall",
		},
		{
			name:     "a file outside the set",
			contents: `{"images":[{"filename":"../escape.png","scale":"1x","size":"16x16"}]}`,
			want:     "not a file inside the icon set",
		},
		{
			name:     "an absolute path",
			contents: `{"images":[{"filename":"/etc/hosts","scale":"1x","size":"16x16"}]}`,
			want:     "not a file inside the icon set",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.contents != "" {
				writeContents(t, dir, tc.contents)
			}
			for name, img := range tc.files {
				writePNG(t, filepath.Join(dir, name), img)
			}
			_, err := appiconset.Build(dir)
			if err == nil {
				t.Fatal("no error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestBuildRefusesAFileThatIsNotAPNG(t *testing.T) {
	dir := t.TempDir()
	writeContents(t, dir, `{"images":[{"filename":"a.png","scale":"1x","size":"16x16"}]}`)
	if err := os.WriteFile(filepath.Join(dir, "a.png"), []byte("not a png"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := appiconset.Build(dir)
	if err == nil || !strings.Contains(err.Error(), "not a readable PNG") {
		t.Errorf("got %v, want a complaint that the file is not a PNG", err)
	}
}

// TestAssetutilSeesTheIcon hands the result to Apple's reader, which is the one
// that decides whether this is a catalog containing an icon or a file that
// merely parses. A catalog with the right images and no descriptor reads back
// perfectly here and still shows the generic bundle icon, so the descriptor is
// looked for explicitly.
func TestAssetutilSeesTheIcon(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("assetutil is macOS-only")
	}
	assetutil, err := exec.LookPath("assetutil")
	if err != nil {
		t.Skip("assetutil is not installed")
	}
	out := filepath.Join(t.TempDir(), "Assets.car")
	if err := appiconset.Compile(writeSet(t), out); err != nil {
		t.Fatal(err)
	}
	raw, err := exec.Command(assetutil, "-I", out).Output()
	if err != nil {
		t.Fatalf("assetutil rejected the catalog: %v", err)
	}
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("assetutil's output is not JSON: %v", err)
	}
	var icons int
	for _, e := range entries {
		if e["AssetType"] == "Icon Image" {
			icons++
			if e["Name"] != appiconset.FacetName {
				t.Errorf("an icon image is named %v, want %s", e["Name"], appiconset.FacetName)
			}
		}
	}
	if want := len(appiconset.Entries()); icons != want {
		t.Errorf("assetutil lists %d icon images, want %d", icons, want)
	}
}
