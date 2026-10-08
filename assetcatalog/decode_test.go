package assetcatalog_test

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/TotallyGamerJet/macbundle/assetcatalog"
	"github.com/TotallyGamerJet/macbundle/bom"
)

// Decoding a rendition back to pixels, which is the check that was missing.
//
// Everything else here asks whether the structure is right — whether the fields
// are in the right places and whether Apple's reader accepts the file. None of
// that catches pixels that decode to the wrong bytes: the icon was stored as a
// zlib stream where CoreUI wanted gzip, with the uncompressed size recorded
// where it wanted the stored one, and assetutil reported ten perfectly good
// renditions while macOS drew noise over black.
//
// So this takes the file apart and reproduces what CoreUI has to do.

// storedPixels pulls the pixel bytes back out of a rendition, undoing whatever
// compression it records.
func storedPixels(t *testing.T, csi []byte) (pixels []byte, compression uint32) {
	t.Helper()
	i := bytes.LastIndex(csi, []byte("MLEC"))
	if i < 0 {
		t.Fatal("the rendition has no CELM payload")
	}
	compression = binary.LittleEndian.Uint32(csi[i+8:])
	recorded := binary.LittleEndian.Uint32(csi[i+12:])
	payload := csi[i+16:]

	// The recorded length is the stored byte count, compressed or not.
	if int(recorded) != len(payload) {
		t.Errorf("the rendition records %d stored bytes but carries %d; "+
			"CoreUI reads that field as a length and walks off the end",
			recorded, len(payload))
	}

	switch compression {
	case 0:
		return payload, compression
	case 2:
		// gzip, not zlib. A zlib stream begins 78 9c and this must begin
		// 1f 8b 08 — CoreUI decodes the wrong one into garbage rather than
		// refusing it.
		if len(payload) < 3 || payload[0] != 0x1f || payload[1] != 0x8b || payload[2] != 0x08 {
			t.Fatalf("compression 2 payload begins %x, want a gzip header (1f 8b 08)",
				payload[:min(4, len(payload))])
		}
		r, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("the payload is not gzip: %v", err)
		}
		out, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("decompressing: %v", err)
		}
		if err := r.Close(); err != nil {
			t.Fatalf("closing the reader: %v", err)
		}
		return out, compression
	default:
		t.Fatalf("the rendition uses compression %d, which this build never writes", compression)
		return nil, 0
	}
}

// wantBGRA is the pixel data a rendition of img must decode to: premultiplied,
// blue first.
func wantBGRA(img image.Image) []byte {
	b := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Src)
	out := make([]byte, 0, b.Dx()*b.Dy()*4)
	for y := range b.Dy() {
		row := rgba.Pix[y*rgba.Stride : y*rgba.Stride+b.Dx()*4]
		for x := 0; x < len(row); x += 4 {
			out = append(out, row[x+2], row[x+1], row[x+0], row[x+3])
		}
	}
	return out
}

// TestRenditionsDecodeToTheOriginalPixels.
//
// Several sizes, because the small ones are stored whole and the large ones
// compressed, and the bug was only in the compressed path.
func TestRenditionsDecodeToTheOriginalPixels(t *testing.T) {
	// Which path a rendition takes is decided by whether gzip makes it smaller,
	// and that is the standard library's business: it has changed between Go
	// releases, so an image that happened to be stored raw under one toolchain
	// was compressed under the next. These inputs are chosen so that the answer
	// does not depend on the compressor. Noise cannot shrink — the gzip framing
	// alone makes it larger — and a smooth gradient always does.
	cases := []struct {
		name string
		img  image.Image
	}{
		{"noise.png", noiseImage(4)},
		{"gradient_64.png", testImage(64)},
		{"gradient_256.png", testImage(256)},
	}
	images := make(map[string]image.Image, len(cases))

	c := &assetcatalog.Catalog{Creator: "macbundle test"}
	for i, tc := range cases {
		images[tc.name] = tc.img
		c.AddRendition(assetcatalog.Rendition{
			Name: tc.name, Image: tc.img, Scale: 1,
			Key: assetcatalog.Key{
				assetcatalog.AttrElement:    85,
				assetcatalog.AttrPart:       220,
				assetcatalog.AttrIdentifier: 6849,
				assetcatalog.AttrDimension2: uint16(i + 1),
				assetcatalog.AttrScale:      1,
			},
		})
	}

	data, err := c.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	r, err := bom.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := r.Tree("RENDITIONS")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(cases) {
		t.Fatalf("the catalog holds %d renditions, want %d", len(entries), len(cases))
	}

	var sawCompressed, sawRaw bool
	for _, e := range entries {
		csi := e.Value
		name := string(bytes.TrimRight(csi[40:168], "\x00"))
		width := binary.LittleEndian.Uint32(csi[12:])
		height := binary.LittleEndian.Uint32(csi[16:])

		img, ok := images[name]
		if !ok {
			t.Errorf("the catalog holds a rendition named %q that was never added", name)
			continue
		}

		got, compression := storedPixels(t, csi)
		switch compression {
		case 0:
			sawRaw = true
		case 2:
			sawCompressed = true
		}

		want := wantBGRA(img)
		if len(got) != len(want) {
			t.Errorf("%s decodes to %d bytes, want %d (%dx%d, four bytes a pixel)",
				name, len(got), len(want), width, height)
			continue
		}
		if !bytes.Equal(got, want) {
			for i := range got {
				if got[i] != want[i] {
					t.Errorf("%s differs from the source image at byte %d (pixel %d): "+
						"got %x, want %x", name, i, i/4, got[i], want[i])
					break
				}
			}
		}
	}

	// If every size took the same path the test proves half of what it should.
	if !sawRaw {
		t.Error("no rendition was stored uncompressed; the raw path is untested")
	}
	if !sawCompressed {
		t.Error("no rendition was compressed; the compressed path is untested")
	}
}

// noiseImage is deterministic pseudo-random pixels: opaque, and incompressible.
func noiseImage(n int) image.Image {
	rng := rand.New(rand.NewPCG(1, 2))
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	for i := range img.Pix {
		img.Pix[i] = uint8(rng.UintN(256))
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 0xFF
	}
	return img
}

// transparentImage is opaque in the middle and clear at the corners, like any
// real icon. The test images elsewhere in this package are fully opaque, which
// is why they never caught what this guards.
func transparentImage(n int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	for y := range n {
		for x := range n {
			a := uint8(255)
			if x < n/4 && y < n/4 {
				a = 0
			}
			img.Set(x, y, color.NRGBA{R: 0x20, G: 0x60, B: 0xE0, A: a})
		}
	}
	return img
}

// TestTransparencySurvives.
//
// CoreUI decided a rendition was opaque and drew its clear margin as
// premultiplied black — a black frame around an icon that then looked too
// small. Nothing in the file said "opaque": the cause was a CELM version this
// build paired with gzip that Apple never pairs with anything flat, and the
// only visible symptom was assetutil reporting Opaque where actool's catalog of
// the same artwork reported it false.
//
// So this asks Apple's reader, on an image that actually has transparency.
func TestTransparencySurvives(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("assetutil is macOS-only")
	}
	assetutil, err := exec.LookPath("assetutil")
	if err != nil {
		t.Skip("assetutil is not installed")
	}

	c := &assetcatalog.Catalog{Creator: "macbundle test"}
	// Large enough that the pixels are compressed, which is where the pairing
	// mattered.
	c.AddRendition(assetcatalog.Rendition{
		Name: "clear.png", Image: transparentImage(128), Scale: 1,
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

	raw, err := exec.Command(assetutil, "-I", path).Output()
	if err != nil {
		t.Fatalf("assetutil refused the catalog: %v", err)
	}
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("unparseable: %v", err)
	}
	var checked int
	for _, e := range entries {
		if e["AssetType"] == nil {
			continue
		}
		checked++
		if opaque, ok := e["Opaque"].(bool); !ok || opaque {
			t.Errorf("assetutil reports Opaque=%v for an image with transparent corners; "+
				"CoreUI will draw the clear area as black", e["Opaque"])
		}
	}
	if checked == 0 {
		t.Fatal("assetutil found no asset to check")
	}
}
