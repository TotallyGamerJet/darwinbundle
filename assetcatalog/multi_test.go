package assetcatalog_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/TotallyGamerJet/macbundle/assetcatalog"
)

// TestAssetutilDistinguishesRenditions.
//
// A catalog with one image reads back correctly; the question is whether a
// catalog with several does. An icon set is ten renditions that differ only in
// two key attributes, and if CoreUI resolves them all to one image the icon is
// wrong at every size but one.
func TestAssetutilDistinguishesRenditions(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("assetutil is macOS-only")
	}
	assetutil, err := exec.LookPath("assetutil")
	if err != nil {
		t.Skip("assetutil is not installed")
	}

	c := &assetcatalog.Catalog{Creator: "macbundle test"}
	c.AddFacet(assetcatalog.Facet{
		Name: "AppIcon",
		Key: assetcatalog.Key{
			assetcatalog.AttrElement:    85,
			assetcatalog.AttrPart:       220,
			assetcatalog.AttrIdentifier: 6849,
		},
	})
	sizes := []int{16, 32, 64, 128}
	for i, px := range sizes {
		c.AddRendition(assetcatalog.Rendition{
			Name:  fmt.Sprintf("icon_%d.png", px),
			Image: testImage(px),
			Scale: 1,
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

	seen := map[float64]bool{}
	for _, e := range entries {
		if e["AssetType"] == nil {
			continue
		}
		seen[e["PixelWidth"].(float64)] = true
	}
	if len(seen) != len(sizes) {
		var got []float64
		for w := range seen {
			got = append(got, w)
		}
		t.Errorf("assetutil sees %d distinct widths %v, want %d — the renditions collapse",
			len(seen), got, len(sizes))
	}
}

// TestTheIconSetDescriptorIsRecognised.
//
// An icon is a set, and a catalog says which sizes the set holds with a
// rendition that carries no pixels: layout 1010, zero dimensions, and a list of
// point sizes with the index each is keyed by.
//
// Leaving it out is not a parse error and does not show up in any comparison of
// the images — every rendition reads back perfectly, assetutil is happy, and
// the application shows the generic bundle icon because IconServices has
// nothing to enumerate. That is exactly what happened, so this asserts Apple's
// reader classifies the descriptor rather than that the bytes look right.
func TestTheIconSetDescriptorIsRecognised(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("assetutil is macOS-only")
	}
	assetutil, err := exec.LookPath("assetutil")
	if err != nil {
		t.Skip("assetutil is not installed")
	}

	key := func(part, dim2, scale uint16) assetcatalog.Key {
		return assetcatalog.Key{
			assetcatalog.AttrElement:    85,
			assetcatalog.AttrPart:       part,
			assetcatalog.AttrIdentifier: 6849,
			assetcatalog.AttrDimension2: dim2,
			assetcatalog.AttrScale:      scale,
		}
	}

	c := &assetcatalog.Catalog{Creator: "macbundle test"}
	c.AddFacet(assetcatalog.Facet{Name: "AppIcon", Key: key(220, 0, 0)})
	c.AddIconSet(assetcatalog.IconSet{
		Name:  "AppIcon",
		Key:   key(218, 0, 1),
		Sizes: []assetcatalog.IconSize{{Points: 16, Index: 1}, {Points: 32, Index: 2}},
	})
	for i, px := range []int{16, 32} {
		c.AddRendition(assetcatalog.Rendition{
			Name: fmt.Sprintf("icon_%d.png", px), Image: testImage(px), Scale: 1,
			Key: key(220, uint16(i+1), 1),
		})
	}

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

	types := map[string]int{}
	for _, e := range entries {
		if s, ok := e["AssetType"].(string); ok {
			types[s]++
		}
	}
	if types["MultiSized Image"] != 1 {
		t.Errorf("assetutil sees %d icon-set descriptors, want 1 — types were %v",
			types["MultiSized Image"], types)
	}
	if types["Icon Image"] != 2 {
		t.Errorf("assetutil sees %d icon images, want 2 — types were %v",
			types["Icon Image"], types)
	}
}

// An icon set has to say something about itself, or the descriptor is a
// rendition with no content that IconServices reads as an empty set.
func TestAnEmptyIconSetIsRefused(t *testing.T) {
	c := &assetcatalog.Catalog{}
	c.AddIconSet(assetcatalog.IconSet{Name: "AppIcon"})
	if _, err := c.Bytes(); err == nil {
		t.Error("an icon set listing no sizes was accepted")
	}
	c = &assetcatalog.Catalog{}
	c.AddIconSet(assetcatalog.IconSet{Sizes: []assetcatalog.IconSize{{Points: 16, Index: 1}}})
	if _, err := c.Bytes(); err == nil {
		t.Error("an icon set with no name was accepted")
	}
}
