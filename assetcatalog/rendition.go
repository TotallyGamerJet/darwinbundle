package assetcatalog

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"image"
	"image/draw"
)

// A rendition is one image in the catalog: the CSI blob a rendition key maps
// to.
//
// The layout below is the one actool produces, field for field, and the test
// beside this file compiles the same image with actool and compares the bytes.
// That is the only way to be sure of a format nobody documents: a catalog that
// is merely plausible parses and shows no icon.
//
//	0    'ISTC'                     magic, little-endian, so 'CTSI' reversed
//	4    version                    1
//	8    rendition flags            0
//	12   width, height
//	20   scale factor               100 per point, so 200 at 2x
//	24   pixel format               'BGRA'
//	28   colour space               1, sRGB
//	32   modification time          actool writes zero
//	36   layout                     12 for an image
//	40   name[128]                  NUL-padded
//	168  info list length
//	172  info list                  a 12-byte prefix and then TLV entries
//	     ...                        one more TLV, outside the list's length
//	     'MLEC'                     the pixel payload
const (
	csiTag     = "ISTC"
	csiVersion = 1

	// pixelFormatBGRA is what CoreUI calls ARGB and stores as BGRA: four
	// 8-bit components, blue first, alpha premultiplied.
	pixelFormatBGRA = "BGRA"

	// colorSpaceSRGB is the identifier actool writes for an sRGB image.
	colorSpaceSRGB = 1

	// layoutImage is the metadata layout for a plain image rendition.
	layoutImage = 12

	// nameLength is the fixed-width name field. CoreUI reads it as a C
	// string, so a name is truncated to leave room for the terminator.
	nameLength = 128
)

// The compression a rendition's pixels are stored under.
//
// actool uses LZFSE, which has no pure-Go encoder worth depending on and would
// put a C toolchain back into a build that has spent a lot of effort removing
// one. Type 2 is in the standard library and CoreUI reads it, which is the
// whole argument: an uncompressed 1024-pixel icon is four megabytes, and the
// same image compressed is a few tens of kilobytes.
//
// Type 2 is *gzip*, not zlib — the payload begins 1f 8b 08 00, not 78 9c. The
// difference is eighteen bytes of framing and it is not cosmetic: CoreUI
// decodes a zlib stream without complaining and produces garbage, so the icon
// renders as noise over black rather than failing. Confirmed by decompressing a
// 1024-pixel rendition out of a system catalog, which gunzips to exactly
// width x height x 4 bytes.
const (
	compressionRaw  = 0
	compressionGzip = 2
)

// celmVersionFor is the payload version that goes with a compression.
//
// It is not a version of this writer: it says something about how the payload
// is framed, and CoreUI pairs particular values with particular compressions.
// Surveying every asset catalog on a system finds versions 1 and 3 only ever
// beside chunked payloads, and 0 and 2 beside flat ones — and every gzip
// rendition Apple ships is version 0.
//
// Writing 2 beside gzip is a pairing Apple never emits, and CoreUI's response
// is not to refuse it: the rendition decodes, assetutil reports it as opaque,
// and the transparent margin around the artwork is drawn as premultiplied
// black. On screen that is a black frame around an icon that looks too small.
func celmVersionFor(compression uint32) uint32 {
	if compression == compressionGzip {
		return 0
	}
	// What actool writes beside uncompressed pixels, and what this package's
	// byte-for-byte comparison against it depends on.
	return 2
}

// Info-list entry tags, named for what their contents turn out to be rather
// than for anything Apple documents.
const (
	tagSliceRect   = 1001
	tagMetrics     = 1003
	tagScaleFactor = 1004
	tagUnknown1006 = 1006

	// tagBytesPerRow is the stride CoreUI hands to the image provider. It read
	// as a constant 64 against the reference, because that catalog held a
	// 16-pixel-wide image and 16 x 4 bytes is also 64. A 32-pixel one is what
	// showed the difference: CoreUI reported "a bad pixel format or failure to
	// create an appropriate image provider for encoding 'uncompressed'" and
	// refused the rendition, while the byte comparison against actool went on
	// passing, since both files agreed on the wrong-for-any-other-size value.
	tagBytesPerRow = 1007
)

// Rendition is one image and the key it is stored under.
type Rendition struct {
	// Name is what assetutil reports as the rendition name. actool uses the
	// source file's name.
	Name string

	// Image is the artwork. Any image is accepted and converted to
	// premultiplied BGRA, which is the only format written here.
	Image image.Image

	// Scale is 1 for a point-for-pixel image and 2 for an @2x one. It is
	// recorded in the key as well as in the header, and CoreUI picks a
	// rendition by it.
	Scale int

	// Key addresses the rendition. Everything the catalog's key format names
	// and this map does not is zero.
	Key Key
}

// AddRendition records an image.
func (c *Catalog) AddRendition(r Rendition) {
	c.renditions = append(c.renditions, r)
}

// encodeCSI lays out one rendition.
func encodeCSI(r Rendition) ([]byte, error) {
	if r.Image == nil {
		return nil, fmt.Errorf("assetcatalog: rendition %q has no image", r.Name)
	}
	scale := r.Scale
	if scale <= 0 {
		scale = 1
	}
	b := r.Image.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("assetcatalog: rendition %q is %dx%d", r.Name, w, h)
	}

	pixels := premultipliedBGRA(r.Image)
	stored, compression, err := compress(pixels)
	if err != nil {
		return nil, fmt.Errorf("assetcatalog: rendition %q: %w", r.Name, err)
	}

	out := make([]byte, 0, 300+len(pixels))
	out = append(out, csiTag...)
	out = binary.LittleEndian.AppendUint32(out, csiVersion)
	out = binary.LittleEndian.AppendUint32(out, 0) // rendition flags
	out = binary.LittleEndian.AppendUint32(out, uint32(w))
	out = binary.LittleEndian.AppendUint32(out, uint32(h))
	out = binary.LittleEndian.AppendUint32(out, uint32(100*scale))
	out = append(out, pixelFormatBGRA...)
	out = binary.LittleEndian.AppendUint32(out, colorSpaceSRGB)

	// Metadata: a modification time actool leaves zero, the layout, and the
	// fixed-width name.
	out = binary.LittleEndian.AppendUint32(out, 0)
	out = binary.LittleEndian.AppendUint16(out, layoutImage)
	out = binary.LittleEndian.AppendUint16(out, 0)
	name := make([]byte, nameLength)
	putFixedString(name, r.Name)
	out = append(out, name...)

	// The payload is a CELM block: a header and the pixels.
	const celmHeader = 16
	payload := make([]byte, 0, celmHeader+len(stored))
	payload = append(payload, "MLEC"...)
	payload = binary.LittleEndian.AppendUint32(payload, celmVersionFor(compression))
	payload = binary.LittleEndian.AppendUint32(payload, compression)
	// The length recorded is the *stored* byte count, compressed or not — the
	// uncompressed size is implied by the dimensions. Recording the pixel count
	// here instead is the other half of the garbled-icon bug: CoreUI reads the
	// field as a length to consume and walks off the end of the payload.
	payload = binary.LittleEndian.AppendUint32(payload, uint32(len(stored)))
	payload = append(payload, stored...)

	// The info list: a prefix whose third word is the size of the CELM block,
	// then the entries. Its recorded length covers the prefix and the entries
	// but not the one entry that follows it — which is how actool writes it,
	// confirmed by the byte comparison in the tests.
	info := make([]byte, 0, 128)
	info = binary.LittleEndian.AppendUint32(info, 1)
	info = binary.LittleEndian.AppendUint32(info, 0)
	info = binary.LittleEndian.AppendUint32(info, uint32(len(payload)))
	info = appendTLV(info, tagSliceRect, le32s(1, 0, 0, uint32(w), uint32(h)))
	info = appendTLV(info, tagMetrics, le32s(1, 0, 0, 0, 0, uint32(w), uint32(h)))
	// A pair of floats: 0 and 1.0. 0x3f800000 is 1.0f.
	info = appendTLV(info, tagScaleFactor, le32s(0, 0x3f800000))
	info = appendTLV(info, tagUnknown1006, le32s(1))

	out = binary.LittleEndian.AppendUint32(out, uint32(len(info)))
	out = append(out, info...)
	out = appendTLV(out, tagBytesPerRow, le32s(uint32(w)*4))
	out = append(out, payload...)
	return out, nil
}

// appendTLV appends one tag/length/value entry.
func appendTLV(b []byte, tag uint32, data []byte) []byte {
	b = binary.LittleEndian.AppendUint32(b, tag)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(data)))
	return append(b, data...)
}

// le32s encodes a run of little-endian 32-bit values.
func le32s(vs ...uint32) []byte {
	b := make([]byte, 0, 4*len(vs))
	for _, v := range vs {
		b = binary.LittleEndian.AppendUint32(b, v)
	}
	return b
}

// premultipliedBGRA converts any image to the one pixel format written here.
//
// Go's RGBA is already premultiplied, so drawing through it does the
// premultiplication and the conversion from whatever the source was in one
// step; the loop only reorders the components.
func premultipliedBGRA(src image.Image) []byte {
	b := src.Bounds()
	rgba, ok := src.(*image.RGBA)
	if !ok || !rgba.Rect.Eq(b) {
		conv := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(conv, conv.Bounds(), src, b.Min, draw.Src)
		rgba = conv
	}

	out := make([]byte, 0, b.Dx()*b.Dy()*4)
	for y := range b.Dy() {
		row := rgba.Pix[y*rgba.Stride : y*rgba.Stride+b.Dx()*4]
		for x := 0; x < len(row); x += 4 {
			out = append(out, row[x+2], row[x+1], row[x+0], row[x+3])
		}
	}
	return out
}

// compress gzips the pixels, falling back to storing them whole if that does
// not actually help — which it will not for an image of a handful of pixels,
// where the framing costs more than the redundancy saves.
//
// The header carries no modification time, so the same artwork always produces
// the same bytes and a rebuild does not churn the bundle.
func compress(pixels []byte) ([]byte, uint32, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(pixels); err != nil {
		return nil, 0, fmt.Errorf("compressing: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, 0, fmt.Errorf("compressing: %w", err)
	}
	if buf.Len() >= len(pixels) {
		return pixels, compressionRaw, nil
	}
	return buf.Bytes(), compressionGzip, nil
}

// The icon-set descriptor.
//
// An icon is not one image but a set of them, and a catalog says so with a
// rendition that carries no pixels at all: layout 1010, zero dimensions, and a
// payload listing the point sizes the set contains and the index each is
// addressed by.
//
// Leaving it out is what makes an application show the generic bundle icon
// while every rendition in the catalog reads back perfectly: the images are
// there and nothing tells IconServices which sizes exist, so it has nothing to
// enumerate. That is the failure this was written to fix.
const (
	// layoutIconSet marks the descriptor rendition.
	layoutIconSet = 1010

	// iconSetTag is 'MSIS', stored little-endian like every other
	// four-character code in these structures.
	iconSetTag = "SISM"

	// tagIconSetPadding is the entry the descriptor carries where a bitmap
	// carries its slice rectangle. Eight zero bytes in every catalog seen.
	tagIconSetPadding = 1004
)

// IconSize is one entry in the descriptor: a size in points, and the index
// renditions of that size are keyed by.
type IconSize struct {
	Points int
	Index  uint16
}

// IconSet is the descriptor for a named icon.
type IconSet struct {
	// Name is the facet name, and what the descriptor rendition is called.
	Name string

	// Key addresses the descriptor. It is the facet's key with the descriptor
	// part rather than the image part.
	Key Key

	// Sizes are the point sizes the set contains, in ascending order.
	Sizes []IconSize
}

// AddIconSet records the descriptor for a named icon.
func (c *Catalog) AddIconSet(s IconSet) {
	c.iconSets = append(c.iconSets, s)
}

// encodeIconSetCSI lays out a descriptor rendition.
//
// It is the same CSI envelope as an image, with the fields that describe pixels
// left zero and an MSIS payload in place of the CELM block.
func encodeIconSetCSI(s IconSet) ([]byte, error) {
	if s.Name == "" {
		return nil, fmt.Errorf("assetcatalog: an icon set needs a name")
	}
	if len(s.Sizes) == 0 {
		return nil, fmt.Errorf("assetcatalog: icon set %q lists no sizes", s.Name)
	}

	// The payload: a version, a count, and three words per size — width,
	// height and the index its renditions are keyed by.
	payload := make([]byte, 0, 12+12*len(s.Sizes))
	payload = append(payload, iconSetTag...)
	payload = binary.LittleEndian.AppendUint32(payload, 1)
	payload = binary.LittleEndian.AppendUint32(payload, uint32(len(s.Sizes)))
	for _, sz := range s.Sizes {
		if sz.Points <= 0 {
			return nil, fmt.Errorf("assetcatalog: icon set %q has a size of %d points",
				s.Name, sz.Points)
		}
		payload = binary.LittleEndian.AppendUint32(payload, uint32(sz.Points))
		payload = binary.LittleEndian.AppendUint32(payload, uint32(sz.Points))
		payload = binary.LittleEndian.AppendUint32(payload, uint32(sz.Index))
	}

	out := make([]byte, 0, 300)
	out = append(out, csiTag...)
	out = binary.LittleEndian.AppendUint32(out, csiVersion)
	out = binary.LittleEndian.AppendUint32(out, 0) // rendition flags
	// Width, height, scale, pixel format and colour space are all zero: this
	// rendition describes a set rather than an image.
	for range 5 {
		out = binary.LittleEndian.AppendUint32(out, 0)
	}
	out = binary.LittleEndian.AppendUint32(out, 0) // modification time
	out = binary.LittleEndian.AppendUint16(out, layoutIconSet)
	out = binary.LittleEndian.AppendUint16(out, 0)
	name := make([]byte, nameLength)
	putFixedString(name, s.Name)
	out = append(out, name...)

	info := make([]byte, 0, 32)
	info = binary.LittleEndian.AppendUint32(info, 1)
	info = binary.LittleEndian.AppendUint32(info, 0)
	info = binary.LittleEndian.AppendUint32(info, uint32(len(payload)))
	info = appendTLV(info, tagIconSetPadding, make([]byte, 8))

	out = binary.LittleEndian.AppendUint32(out, uint32(len(info)))
	out = append(out, info...)
	out = appendTLV(out, tagUnknown1006, le32s(1))
	out = append(out, payload...)
	return out, nil
}
