// Package appiconset compiles a macOS application icon, as Xcode lays it out
// on disk, into the Assets.car that goes in a bundle's Contents/Resources.
//
// An .appiconset is a directory holding one PNG per size and a Contents.json
// saying which is which. Xcode compiles it with actool; Compile does the same
// with nothing installed but a Go toolchain, using package assetcatalog.
//
// The set is read from disk rather than described in Go, so that replacing the
// artwork is a matter of dropping in new PNGs and editing Contents.json, which
// is what an .appiconset is for.
//
// # What macOS asks for
//
// Entries lists the ten images a complete macOS icon contains. A size left out
// is not an error: macOS falls back to scaling a larger one, which looks soft,
// and the small sizes especially are worth drawing rather than resampling.
// Contents generates a Contents.json naming all ten, for a tool that produces
// an icon set rather than consuming one.
package appiconset

import (
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/TotallyGamerJet/macbundle/assetcatalog"
)

// FacetName is the name macOS looks the icon up by, and the value that belongs
// in a bundle's CFBundleIconName.
const FacetName = "AppIcon"

// The attribute values an application icon is stored under. They are not
// arbitrary: CoreUI looks an icon up by them, and they were read out of a
// catalog actool compiled from an icon set.
const (
	iconElement = 85

	// An icon has two parts in a catalog: the images, and a descriptor saying
	// which sizes the set contains. Without the descriptor the images are all
	// present and IconServices has nothing to enumerate, so the application
	// shows the generic bundle icon.
	iconPartImage      = 220
	iconPartDescriptor = 218

	iconIdentifier = 6849
)

// creator is recorded in the catalog header as the authoring tool.
const creator = "macbundle"

// Entry is one image in a macOS application icon set: a nominal size in points
// and a scale, which together give the pixel size.
//
// Both matter. macOS picks by point size and display scale, so a 32-pixel image
// is needed twice — once as 32pt at 1x and once as 16pt at 2x — and the two are
// different entries even though the pixels are identical.
type Entry struct {
	Points int
	Scale  int
	File   string
}

// Pixels is the edge length of the image this entry needs.
func (e Entry) Pixels() int { return e.Points * e.Scale }

var entries = [...]Entry{
	{Points: 16, Scale: 1, File: "icon_16x16.png"},
	{Points: 16, Scale: 2, File: "icon_16x16@2x.png"},
	{Points: 32, Scale: 1, File: "icon_32x32.png"},
	{Points: 32, Scale: 2, File: "icon_32x32@2x.png"},
	{Points: 128, Scale: 1, File: "icon_128x128.png"},
	{Points: 128, Scale: 2, File: "icon_128x128@2x.png"},
	{Points: 256, Scale: 1, File: "icon_256x256.png"},
	{Points: 256, Scale: 2, File: "icon_256x256@2x.png"},
	{Points: 512, Scale: 1, File: "icon_512x512.png"},
	{Points: 512, Scale: 2, File: "icon_512x512@2x.png"},
}

// Entries returns the images a complete macOS application icon contains, in
// ascending size. The slice is a copy.
func Entries() []Entry { return slices.Clone(entries[:]) }

// contents is the part of an .appiconset's Contents.json that is read and
// written here.
type contents struct {
	Images []imageEntry `json:"images"`
	Info   *info        `json:"info,omitempty"`
}

type imageEntry struct {
	Filename string `json:"filename,omitempty"`
	Idiom    string `json:"idiom"`
	Scale    string `json:"scale"`
	Size     string `json:"size"`
}

type info struct {
	Author  string `json:"author"`
	Version int    `json:"version"`
}

// Contents renders a Contents.json naming every entry in Entries, for an icon
// set being produced rather than read. author is recorded in the file's info
// block; Xcode shows it nowhere and nothing depends on it.
func Contents(author string) []byte {
	c := contents{Info: &info{Author: author, Version: 1}}
	for _, e := range entries {
		c.Images = append(c.Images, imageEntry{
			Filename: e.File,
			Idiom:    "mac",
			Scale:    fmt.Sprintf("%dx", e.Scale),
			Size:     fmt.Sprintf("%dx%d", e.Points, e.Points),
		})
	}
	// Marshalling this structure cannot fail: it holds only strings and ints.
	out, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		panic("appiconset: marshalling Contents.json: " + err.Error())
	}
	return append(out, '\n')
}

// Compile reads the .appiconset at setDir and writes the compiled asset catalog
// to outPath.
func Compile(setDir, outPath string) error {
	data, err := Build(setDir)
	if err != nil {
		return err
	}
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		return fmt.Errorf("appiconset: writing %s: %w", outPath, err)
	}
	return nil
}

// Build reads the .appiconset at setDir and returns the compiled asset catalog.
//
// Every image named in Contents.json must exist, be a PNG, and be exactly as
// many pixels wide as its size and scale say. An entry with no filename is a
// size the designer has not drawn yet, which Xcode allows, and contributes
// nothing; a set in which every entry is like that is an error.
func Build(setDir string) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(setDir, "Contents.json"))
	if err != nil {
		return nil, fmt.Errorf("appiconset: reading the icon set: %w", err)
	}
	var c contents
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("appiconset: %s/Contents.json is not valid JSON: %w", setDir, err)
	}

	type loaded struct {
		points, scale int
		file          string
		img           image.Image
	}
	var imgs []loaded
	seen := map[int]bool{}

	for _, e := range c.Images {
		if e.Filename == "" {
			continue
		}
		// Contents.json names files inside the set. One that climbs out of it
		// is either a mistake or an attempt to make a build read a file from
		// somewhere else, and neither is something to honour.
		if !filepath.IsLocal(e.Filename) {
			return nil, fmt.Errorf("appiconset: %q is not a file inside the icon set", e.Filename)
		}
		pt, err := parsePoints(e.Size)
		if err != nil {
			return nil, fmt.Errorf("appiconset: %s: %w", e.Filename, err)
		}
		scale, err := parseScale(e.Scale)
		if err != nil {
			return nil, fmt.Errorf("appiconset: %s: %w", e.Filename, err)
		}
		img, err := readPNG(filepath.Join(setDir, e.Filename))
		if err != nil {
			return nil, err
		}
		if got := img.Bounds().Dx(); got != pt*scale {
			return nil, fmt.Errorf("appiconset: %s is %d pixels wide but %s at %s means %d",
				e.Filename, got, e.Size, e.Scale, pt*scale)
		}
		if got := img.Bounds().Dy(); got != pt*scale {
			return nil, fmt.Errorf("appiconset: %s is %d pixels tall but %s at %s means %d",
				e.Filename, got, e.Size, e.Scale, pt*scale)
		}
		imgs = append(imgs, loaded{points: pt, scale: scale, file: e.Filename, img: img})
		seen[pt] = true
	}
	if len(imgs) == 0 {
		return nil, fmt.Errorf("appiconset: %s contains no images", setDir)
	}

	// CoreUI addresses each size by an index rather than by its point size, so
	// the sizes are numbered from one in ascending order — which is what actool
	// does, and what a catalog compiled from the same set contains.
	sizes := make([]int, 0, len(seen))
	for pt := range seen {
		sizes = append(sizes, pt)
	}
	slices.Sort(sizes)
	index := make(map[int]uint16, len(sizes))
	for i, pt := range sizes {
		index[pt] = uint16(i + 1)
	}

	cat := &assetcatalog.Catalog{Creator: creator}
	cat.AddFacet(assetcatalog.Facet{
		Name: FacetName,
		Key: assetcatalog.Key{
			assetcatalog.AttrElement:    iconElement,
			assetcatalog.AttrPart:       iconPartImage,
			assetcatalog.AttrIdentifier: iconIdentifier,
		},
	})

	// The descriptor: which sizes the set contains, and the index each is
	// addressed by. It carries no pixels.
	set := assetcatalog.IconSet{
		Name: FacetName,
		Key: assetcatalog.Key{
			assetcatalog.AttrElement:    iconElement,
			assetcatalog.AttrPart:       iconPartDescriptor,
			assetcatalog.AttrIdentifier: iconIdentifier,
			assetcatalog.AttrScale:      1,
		},
	}
	for _, pt := range sizes {
		set.Sizes = append(set.Sizes, assetcatalog.IconSize{Points: pt, Index: index[pt]})
	}
	cat.AddIconSet(set)

	for _, l := range imgs {
		cat.AddRendition(assetcatalog.Rendition{
			Name:  l.file,
			Image: l.img,
			Scale: l.scale,
			Key: assetcatalog.Key{
				assetcatalog.AttrElement:    iconElement,
				assetcatalog.AttrPart:       iconPartImage,
				assetcatalog.AttrIdentifier: iconIdentifier,
				assetcatalog.AttrDimension2: index[l.points],
				assetcatalog.AttrScale:      uint16(l.scale),
			},
		})
	}

	out, err := cat.Bytes()
	if err != nil {
		return nil, fmt.Errorf("appiconset: building the asset catalog: %w", err)
	}
	return out, nil
}

// parsePoints reads the "16x16" form.
func parsePoints(size string) (int, error) {
	w, _, ok := strings.Cut(size, "x")
	if !ok {
		return 0, fmt.Errorf("size %q is not of the form 16x16", size)
	}
	n, err := strconv.Atoi(w)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("size %q is not of the form 16x16", size)
	}
	return n, nil
}

// parseScale reads the "2x" form.
func parseScale(scale string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSuffix(scale, "x"))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("scale %q is not of the form 2x", scale)
	}
	return n, nil
}

func readPNG(path string) (_ image.Image, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("appiconset: reading icon artwork: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("appiconset: closing %s: %w", path, cerr)
		}
	}()
	img, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("appiconset: %s is not a readable PNG: %w", path, err)
	}
	return img, nil
}
