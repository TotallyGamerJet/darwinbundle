package assetcatalog_test

import (
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/TotallyGamerJet/darwinbundle/assetcatalog"
)

func pixel() image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.NRGBA{R: 1, G: 2, B: 3, A: 255})
	return img
}

// A key is written as one 16-bit value per attribute *in the key format*. An
// attribute the format does not list has nowhere to go, and used to be dropped
// without a word, which stores the image at an address other than the one the
// caller asked for.
func TestAKeyNamingAnAttributeTheFormatLacksIsRefused(t *testing.T) {
	c := &assetcatalog.Catalog{
		KeyFormat: []assetcatalog.Attribute{assetcatalog.AttrElement, assetcatalog.AttrPart},
	}
	c.AddRendition(assetcatalog.Rendition{
		Name: "icon.png", Image: pixel(), Scale: 2,
		Key: assetcatalog.Key{
			assetcatalog.AttrElement: 85,
			assetcatalog.AttrScale:   2, // not in the format above
		},
	})
	_, err := c.Bytes()
	if err == nil {
		t.Fatal("a key using an attribute outside the key format was accepted")
	}
	if !strings.Contains(err.Error(), "icon.png") {
		t.Errorf("the error does not name the rendition: %v", err)
	}
}

func TestTwoRenditionsWithOneKeyAreNamed(t *testing.T) {
	key := assetcatalog.Key{assetcatalog.AttrElement: 85, assetcatalog.AttrScale: 1}
	c := &assetcatalog.Catalog{}
	c.AddRendition(assetcatalog.Rendition{Name: "first.png", Image: pixel(), Scale: 1, Key: key})
	c.AddRendition(assetcatalog.Rendition{Name: "second.png", Image: pixel(), Scale: 1, Key: key})

	_, err := c.Bytes()
	if err == nil {
		t.Fatal("two renditions with the same key were accepted")
	}
	for _, name := range []string{"first.png", "second.png"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the error does not name %s: %v", name, err)
		}
	}
}

// vast reports bounds it has no pixels for, which is how to ask whether the
// size limit is enforced without allocating the memory it protects.
type vast struct{ w, h int }

func (v vast) ColorModel() color.Model { return color.NRGBAModel }
func (v vast) Bounds() image.Rectangle { return image.Rect(0, 0, v.w, v.h) }
func (v vast) At(int, int) color.Color { return color.NRGBA{} }

// Pixel data is recorded with a 32-bit length. An image past that would be
// written with a wrapped one, and CoreUI would read it as a length to consume.
func TestAnImageTooLargeToRecordIsRefused(t *testing.T) {
	c := &assetcatalog.Catalog{}
	c.AddRendition(assetcatalog.Rendition{
		Name: "huge.png", Image: vast{w: 1 << 16, h: 1 << 16}, Scale: 1,
		Key: assetcatalog.Key{assetcatalog.AttrElement: 85},
	})
	if _, err := c.Bytes(); err == nil {
		t.Error("an image whose pixels cannot be recorded in 32 bits was accepted")
	}
}

func TestAFacetKeyWithAnUnknownAttributeIsRefused(t *testing.T) {
	c := &assetcatalog.Catalog{}
	c.AddFacet(assetcatalog.Facet{
		Name: "AppIcon",
		Key:  assetcatalog.Key{assetcatalog.Attribute(999): 1},
	})
	if _, err := c.Bytes(); err == nil {
		t.Error("a facet key using an attribute this package cannot encode was accepted")
	}
}
