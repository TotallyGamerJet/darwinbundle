// Package assetcatalog writes the CoreUI structures that make a BOM container
// a compiled asset catalog: an Assets.car.
//
// The container itself is package bom. This package is what goes inside it: the
// header CoreUI reads first, the key format that says how a rendition is
// addressed, and the two trees that map a name to a key and a key to an image.
//
// It is what Xcode's actool does for an application icon, in Go, so that a
// distribution build needs nothing but a Go toolchain. It does not try to be all
// of actool: every image is stored whole as premultiplied BGRA, gzip-compressed
// when that helps, where actool packs small icons into an atlas and compresses
// the rest with LZFSE. The result is larger on disk and identical on screen.
//
// # Endianness
//
// The container is big-endian and everything here is little-endian. That is not
// a mistake in one of them; it is the boundary between a NeXT-era archive
// format and structures CoreUI memory-maps on a little-endian machine. Every
// integer written by this package is little-endian, and every offset the
// container records is big-endian.
//
// # Provenance
//
// The layouts were read out of catalogs actool produced and cross-checked
// against assetutil's decoding of them, not guessed. On macOS the tests repeat
// that comparison: they compile the same artwork with actool and compare bytes,
// and hand what this package wrote to assetutil. Those tests skip where the
// tools are absent, so elsewhere only the structural and round-trip checks run.
//
// # Reproducibility
//
// The same catalog built by the same Go toolchain is byte-for-byte identical:
// nothing in the output depends on the clock, the host, or map iteration order.
// Compression is the standard library's gzip, though, and its output is allowed
// to change between Go releases, so two toolchains can write different bytes
// for the same artwork. Both decode to the same pixels.
//
// Nothing in a catalog's structure tells you the pixels are right. A catalog
// can parse, list every rendition at the right size and scale, and still draw
// noise; the tests therefore also take renditions apart and compare their
// decoded pixels with the source image.
package assetcatalog

import (
	"encoding/binary"
	"fmt"
	"io"
	"slices"

	"github.com/TotallyGamerJet/darwinbundle/bom"
)

// Variable names CoreUI looks for in the container.
const (
	varHeader     = "CARHEADER"
	varKeyFormat  = "KEYFORMAT"
	varFacetKeys  = "FACETKEYS"
	varRenditions = "RENDITIONS"
)

// headerTag and keyFormatTag are the four-character codes at the start of the
// two fixed structures, stored little-endian — so 'CTAR' reads as "RATC" in a
// hex dump, which is what a reference catalog contains.
const (
	headerTag    = "RATC"
	keyFormatTag = "tmfk"
)

// CoreUIVersion and StorageVersion are what the reference catalog carries.
//
// They are a statement about the format written, not about the machine writing
// it: CoreUI refuses a storage version it does not recognise, and 17 is what
// every catalog on a current system uses.
const (
	CoreUIVersion  uint32 = 975
	StorageVersion uint32 = 17
	SchemaVersion  uint32 = 2
)

// Attribute is one component of a rendition key. CoreUI addresses every image
// by a fixed tuple of these, and the key format says which ones and in what
// order.
type Attribute uint32

// The attributes a catalog of application icons needs. The numbering is
// CoreUI's; the names are the ones assetutil prints.
const (
	AttrElement    Attribute = 1  // kCRThemeElementName
	AttrPart       Attribute = 2  // kCRThemePartName
	AttrSize       Attribute = 3  // kCRThemeSizeName
	AttrAppearance Attribute = 7  // kCRThemeAppearanceName
	AttrDimension2 Attribute = 9  // kCRThemeDimension2Name
	AttrLayer      Attribute = 11 // kCRThemeLayerName
	AttrScale      Attribute = 12 // kCRThemeScaleName
	AttrLocale     Attribute = 13 // kCRThemeLocalizationName
	AttrIdentifier Attribute = 17 // kCRThemeIdentifierName
)

// defaultKeyFormat is the attribute order actool writes for a macOS catalog.
//
// The order is load-bearing rather than cosmetic: a rendition's key is just a
// sequence of 16-bit values, and which value means what is decided entirely by
// this list. Two catalogs with the same renditions and different key formats
// describe different images.
var defaultKeyFormat = [...]Attribute{
	AttrAppearance, AttrLocale, AttrElement, AttrPart, AttrSize,
	AttrIdentifier, AttrDimension2, AttrLayer, AttrScale,
}

// DefaultKeyFormat returns the attribute order actool writes for a macOS
// catalog, and the one a Catalog uses when its KeyFormat is empty.
//
// It returns a copy: the order is what gives every rendition key its meaning,
// so it is not something a caller should be able to edit in place.
func DefaultKeyFormat() []Attribute { return slices.Clone(defaultKeyFormat[:]) }

// Key is one rendition's address: a value for each attribute in the catalog's
// key format.
type Key map[Attribute]uint16

// Facet is a named asset — "AppIcon" — and the partial key that identifies the
// group of renditions belonging to it.
type Facet struct {
	Name string
	Key  Key
}

// Catalog is a set of facets and renditions being assembled.
type Catalog struct {
	// KeyFormat is the attribute order every rendition key is written in.
	// Empty means DefaultKeyFormat().
	KeyFormat []Attribute

	// Creator is recorded in the header and shown by assetutil as the
	// authoring tool. It is documentation, not a version check.
	Creator string

	facets     []Facet
	renditions []Rendition
	iconSets   []IconSet
}

// AddFacet records a named asset.
func (c *Catalog) AddFacet(f Facet) {
	c.facets = append(c.facets, f)
}

func (c *Catalog) keyFormat() []Attribute {
	if len(c.KeyFormat) == 0 {
		return defaultKeyFormat[:]
	}
	return c.KeyFormat
}

// Bytes encodes the catalog as the contents of an Assets.car file.
func (c *Catalog) Bytes() ([]byte, error) {
	w, err := c.container()
	if err != nil {
		return nil, err
	}
	return w.Bytes()
}

// WriteTo writes the catalog as the contents of an Assets.car file. It
// implements io.WriterTo.
func (c *Catalog) WriteTo(out io.Writer) (int64, error) {
	w, err := c.container()
	if err != nil {
		return 0, err
	}
	return w.WriteTo(out)
}

// container lays the catalog out as a BOM container ready to be written.
func (c *Catalog) container() (*bom.Writer, error) {
	w := bom.NewWriter()
	format := c.keyFormat()

	total := uint32(len(c.renditions) + len(c.iconSets))
	if err := w.SetVariable(varHeader, w.AddBlock(c.header(total))); err != nil {
		return nil, err
	}
	if err := w.SetVariable(varKeyFormat, w.AddBlock(encodeKeyFormat(format))); err != nil {
		return nil, err
	}

	facets := make([]bom.TreeEntry, 0, len(c.facets))
	for _, f := range c.facets {
		if f.Name == "" {
			return nil, fmt.Errorf("assetcatalog: a facet needs a name")
		}
		for a := range f.Key {
			if !slices.Contains(attributeOrder, a) {
				return nil, fmt.Errorf("assetcatalog: facet %q uses attribute %d, which cannot be encoded", f.Name, a)
			}
		}
		facets = append(facets, bom.TreeEntry{
			Key:   []byte(f.Name),
			Value: encodeFacetValue(f.Key),
		})
	}
	if err := w.AddTree(varFacetKeys, bom.DefaultBlockSize, facets); err != nil {
		return nil, err
	}

	renditions := make([]bom.TreeEntry, 0, len(c.renditions)+len(c.iconSets))
	owner := make(map[string]string) // encoded key -> the name that claimed it
	add := func(name string, key Key, csi []byte) error {
		if err := checkKey(format, key); err != nil {
			return fmt.Errorf("assetcatalog: %q: %w", name, err)
		}
		k := encodeRenditionKey(format, key)
		if first, taken := owner[string(k)]; taken {
			return fmt.Errorf("assetcatalog: %q and %q have the same key, so one would overwrite the other",
				first, name)
		}
		owner[string(k)] = name
		renditions = append(renditions, bom.TreeEntry{Key: k, Value: csi})
		return nil
	}
	for _, s := range c.iconSets {
		csi, err := encodeIconSetCSI(s)
		if err != nil {
			return nil, err
		}
		if err := add(s.Name, s.Key, csi); err != nil {
			return nil, err
		}
	}
	for _, r := range c.renditions {
		csi, err := encodeCSI(r)
		if err != nil {
			return nil, err
		}
		if err := add(r.Name, r.Key, csi); err != nil {
			return nil, err
		}
	}
	if err := w.AddTree(varRenditions, bom.DefaultBlockSize, renditions); err != nil {
		return nil, err
	}
	return w, nil
}

// header encodes the 436-byte CARHEADER.
//
// The two strings are fixed-width fields rather than anything length-prefixed,
// and CoreUI reads them as C strings, so they are truncated to leave room for
// the terminator rather than filling the field.
func (c *Catalog) header(renditions uint32) []byte {
	const (
		mainVersionLen = 128
		versionLen     = 256
		size           = 436
	)
	b := make([]byte, size)
	copy(b, headerTag)
	binary.LittleEndian.PutUint32(b[4:], CoreUIVersion)
	binary.LittleEndian.PutUint32(b[8:], StorageVersion)
	binary.LittleEndian.PutUint32(b[12:], 0) // storage timestamp; actool writes zero
	binary.LittleEndian.PutUint32(b[16:], renditions)

	putFixedString(b[20:20+mainVersionLen], "@(#)PROGRAM:CoreUI  PROJECT:CoreUI-975 [LAR]")
	putFixedString(b[148:148+versionLen], c.Creator)
	// b[404:420] is a UUID actool leaves zero.
	binary.LittleEndian.PutUint32(b[420:], 0)             // associated checksum
	binary.LittleEndian.PutUint32(b[424:], SchemaVersion) //
	binary.LittleEndian.PutUint32(b[428:], 1)             // colour space: sRGB
	binary.LittleEndian.PutUint32(b[432:], 1)             // key semantics
	return b
}

// putFixedString copies s into a fixed-width field, always leaving it
// NUL-terminated.
func putFixedString(field []byte, s string) {
	if len(s) >= len(field) {
		s = s[:len(field)-1]
	}
	copy(field, s)
}

// encodeKeyFormat encodes the attribute order: the tag, a version, the number
// of attributes, and then the attributes themselves.
func encodeKeyFormat(format []Attribute) []byte {
	b := make([]byte, 0, 12+4*len(format))
	b = append(b, keyFormatTag...)
	b = binary.LittleEndian.AppendUint32(b, 0) // version
	b = binary.LittleEndian.AppendUint32(b, uint32(len(format)))
	for _, a := range format {
		b = binary.LittleEndian.AppendUint32(b, uint32(a))
	}
	return b
}

// encodeFacetValue encodes a facet's partial key: a pair of 16-bit values a
// cursor would use for its hot spot and everything else leaves zero, the number
// of attributes, and then (attribute, value) pairs.
//
// The pairs are written in the catalog's key-format order so that two facets
// built from equal maps encode identically, which Go's map iteration would
// otherwise not guarantee.
func encodeFacetValue(key Key) []byte {
	b := make([]byte, 0, 6+4*len(key))
	b = binary.LittleEndian.AppendUint16(b, 0) // hot spot x
	b = binary.LittleEndian.AppendUint16(b, 0) // hot spot y
	b = binary.LittleEndian.AppendUint16(b, uint16(len(key)))
	for _, a := range attributeOrder {
		v, ok := key[a]
		if !ok {
			continue
		}
		b = binary.LittleEndian.AppendUint16(b, uint16(a))
		b = binary.LittleEndian.AppendUint16(b, v)
	}
	return b
}

// checkKey reports an attribute in key that the key format has no slot for.
// encodeRenditionKey writes only the attributes the format lists, so without
// this the value would be dropped and the image stored at a different address
// from the one asked for.
func checkKey(format []Attribute, key Key) error {
	for a := range key {
		if !slices.Contains(format, a) {
			return fmt.Errorf("the key sets attribute %d, which is not in the catalog's key format", a)
		}
	}
	return nil
}

// encodeRenditionKey writes one 16-bit value per attribute in the key format,
// in that order. The format is the only thing that says which value means what,
// so a key is meaningless without it.
func encodeRenditionKey(format []Attribute, key Key) []byte {
	b := make([]byte, 0, 2*len(format))
	for _, a := range format {
		b = binary.LittleEndian.AppendUint16(b, key[a])
	}
	return b
}

// attributeOrder is every attribute this package knows, in numeric order, so
// that encoding a key is deterministic.
var attributeOrder = []Attribute{
	AttrElement, AttrPart, AttrSize, AttrAppearance,
	AttrDimension2, AttrLayer, AttrScale, AttrLocale, AttrIdentifier,
}
