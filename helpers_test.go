package darwinbundle_test

import (
	"encoding/xml"
	"errors"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// readPlist parses an XML property list into Go values: dict as
// map[string]any, array as []any, string, integer as int, and true/false as
// bool. It exists so tests assert on what an Info.plist says rather than on how
// it is spelled, and it works on any platform.
func readPlist(t testing.TB, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec := xml.NewDecoder(strings.NewReader(string(raw)))
	for {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("%s: no <dict> found: %v", path, err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "dict" {
			v, err := parseDict(dec)
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			return v
		}
	}
}

func parseDict(dec *xml.Decoder) (map[string]any, error) {
	out := map[string]any{}
	var key string
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch tt := tok.(type) {
		case xml.EndElement:
			return out, nil
		case xml.StartElement:
			if tt.Name.Local == "key" {
				var k string
				if err := dec.DecodeElement(&k, &tt); err != nil {
					return nil, err
				}
				key = k
				continue
			}
			v, err := parseValue(dec, tt)
			if err != nil {
				return nil, err
			}
			out[key] = v
		}
	}
}

func parseValue(dec *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "dict":
		return parseDict(dec)
	case "array":
		arr := []any{}
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch tt := tok.(type) {
			case xml.EndElement:
				return arr, nil
			case xml.StartElement:
				v, err := parseValue(dec, tt)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
		}
	case "true":
		return true, dec.Skip()
	case "false":
		return false, dec.Skip()
	case "integer":
		var s string
		if err := dec.DecodeElement(&s, &start); err != nil {
			return nil, err
		}
		return strconv.Atoi(s)
	default: // string, real, date, data
		var s string
		if err := dec.DecodeElement(&s, &start); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		return s, nil
	}
}

// fakeBinary writes an executable that is not a real Mach-O, which is all
// assembling a bundle needs: it is copied, not read.
func fakeBinary(t testing.TB, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// iconSet lays out a minimal icon set: one drawn size, which is all a catalog
// needs to be valid.
func iconSet(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for i := range img.Pix {
		img.Pix[i] = 0xC0
	}
	f, err := os.Create(filepath.Join(dir, "icon_16x16.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	contents := `{"images":[{"filename":"icon_16x16.png","idiom":"mac","scale":"1x","size":"16x16"}]}`
	if err := os.WriteFile(filepath.Join(dir, "Contents.json"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}
