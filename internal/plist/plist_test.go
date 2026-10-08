package plist_test

import (
	"encoding/xml"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/TotallyGamerJet/darwinbundle/internal/plist"
)

func marshal(t *testing.T, root map[string]any) string {
	t.Helper()
	b, err := plist.Marshal(root)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return string(b)
}

func TestMarshalIsApplesCanonicalForm(t *testing.T) {
	got := marshal(t, map[string]any{
		"Name":    "x",
		"Count":   3,
		"Enabled": true,
		"Off":     false,
		"Nested":  map[string]any{"Inner": "y"},
		"List":    []string{"a", "b"},
		"Empty":   map[string]any{},
		"None":    []any{},
	})
	want := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Count</key>
	<integer>3</integer>
	<key>Empty</key>
	<dict/>
	<key>Enabled</key>
	<true/>
	<key>List</key>
	<array>
		<string>a</string>
		<string>b</string>
	</array>
	<key>Name</key>
	<string>x</string>
	<key>Nested</key>
	<dict>
		<key>Inner</key>
		<string>y</string>
	</dict>
	<key>None</key>
	<array/>
	<key>Off</key>
	<false/>
</dict>
</plist>
`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestTheSameInputIsTheSameBytes(t *testing.T) {
	root := map[string]any{}
	for _, k := range strings.Split("q w e r t y u i o p a s d f g h j k l", " ") {
		root[k] = k
	}
	first := marshal(t, root)
	for range 25 {
		if marshal(t, root) != first {
			t.Fatal("two renderings of one value differed, so map order leaked into the output")
		}
	}
}

func TestEveryNumericTypeIsAnInteger(t *testing.T) {
	got := marshal(t, map[string]any{
		"a": int8(-1), "b": int16(2), "c": int32(3), "d": int64(math.MinInt64),
		"e": uint8(4), "f": uint16(5), "g": uint32(6), "h": uint64(math.MaxInt64), "i": uint(7),
	})
	for _, want := range []string{
		"<integer>-1</integer>", "<integer>2</integer>", "<integer>3</integer>",
		"<integer>-9223372036854775808</integer>", "<integer>4</integer>", "<integer>5</integer>",
		"<integer>6</integer>", "<integer>9223372036854775807</integer>", "<integer>7</integer>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
}

func TestOtherScalarKinds(t *testing.T) {
	when := time.Date(2024, 3, 9, 14, 5, 6, 0, time.FixedZone("x", 5*3600))
	got := marshal(t, map[string]any{
		"real":  1.5,
		"small": float32(0.25),
		"data":  []byte("hello"),
		"date":  when,
	})
	for _, want := range []string{
		"<real>1.5</real>",
		"<real>0.25</real>",
		"<data>\n\taGVsbG8=\n\t</data>",
		// Written in UTC whatever zone the value carries.
		"<date>2024-03-09T09:05:06Z</date>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
}

func TestSlicesAndMapsOfConcreteTypes(t *testing.T) {
	got := marshal(t, map[string]any{
		"docs": []map[string]any{{"Name": "A"}, {"Name": "B"}},
		"ints": []int{1, 2},
		"by":   map[string]string{"k": "v"},
		"arr":  [2]string{"x", "y"},
	})
	for _, want := range []string{
		"<key>Name</key>\n\t\t\t<string>A</string>",
		"<integer>2</integer>",
		"<key>k</key>\n\t\t<string>v</string>",
		"<string>y</string>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestMarkupIsEscaped(t *testing.T) {
	got := marshal(t, map[string]any{"a&b": "<x> & </x>"})
	for _, want := range []string{"<key>a&amp;b</key>", "<string>&lt;x&gt; &amp; &lt;/x&gt;</string>"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// What cannot be written is an error that says where it was found, not a
// guess. The original of this encoder wrote any type it did not recognise as a
// <string> of its default formatting, so an int64 or a float came out as text.
func TestWhatAPlistCannotHoldIsRefused(t *testing.T) {
	type custom struct{ A int }
	tests := []struct {
		name  string
		value any
		path  string
	}{
		{"nil", nil, "root.k"},
		{"nil inside a list", []any{"ok", nil}, "root.k[1]"},
		{"a nil pointer", (*string)(nil), "root.k"},
		{"a struct", custom{1}, "root.k"},
		{"a channel", make(chan int), "root.k"},
		{"a func", func() {}, "root.k"},
		{"NaN", math.NaN(), "root.k"},
		{"infinity", math.Inf(1), "root.k"},
		{"an integer too big for a plist", uint64(math.MaxInt64) + 1, "root.k"},
		{"a map keyed by int", map[int]string{1: "a"}, "root.k"},
		{"a nested bad value", map[string]any{"deep": custom{}}, "root.k.deep"},
		{"a NUL", "a\x00b", "root.k"},
		{"a control character", "a\x01b", "root.k"},
		{"invalid UTF-8", "a\xffb", "root.k"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := plist.Marshal(map[string]any{"k": tc.value})
			if err == nil {
				t.Fatal("no error")
			}
			if !strings.Contains(err.Error(), tc.path) {
				t.Errorf("error %q does not say where (%s)", err, tc.path)
			}
		})
	}
}

func TestAControlCharacterInAKeyIsRefused(t *testing.T) {
	if _, err := plist.Marshal(map[string]any{"bad\x01key": "v"}); err == nil {
		t.Error("a key XML cannot carry was accepted")
	}
}

func TestTabsNewlinesAndNonASCIISurvive(t *testing.T) {
	got := marshal(t, map[string]any{"k": "tab\there\nnew é 日本 😀"})
	if !strings.Contains(got, "<string>tab\there\nnew é 日本 😀</string>") {
		t.Errorf("text was altered:\n%s", got)
	}
}

// TestPlutilAgrees asks the platform's own tool to read what was written and
// write it back out. Apple's canonical form and ours are the same bytes, which
// is a stronger statement than "plutil does not complain".
func TestPlutilAgrees(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("plutil is macOS-only")
	}
	plutil, err := exec.LookPath("plutil")
	if err != nil {
		t.Skip("plutil is not installed")
	}

	root := map[string]any{
		"CFBundleName": "My <App> & Co",
		"Flag":         true,
		"Count":        42,
		"Ratio":        1.5,
		"Blob":         []byte("some bytes"),
		"Long": func() []byte {
			b := make([]byte, 200)
			for i := range b {
				b[i] = byte(i)
			}
			return b
		}(),
		"Nothing": []byte{},
		"When":    time.Date(2024, 3, 9, 14, 5, 6, 0, time.UTC),
		"List":    []any{"a", 1, true, map[string]any{"k": "v"}, []string{}},
		"Nested": map[string]any{
			"Empty": map[string]any{},
			"Deep":  map[string]any{"x": []string{"y"}},
		},
		"Unicode": "é 日本 😀",
	}
	ours := marshal(t, root)

	path := filepath.Join(t.TempDir(), "Info.plist")
	if err := os.WriteFile(path, []byte(ours), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(plutil, "-lint", path).CombinedOutput(); err != nil {
		t.Fatalf("plutil -lint rejected the output: %s\n%s", out, ours)
	}
	canonical, err := exec.Command(plutil, "-convert", "xml1", "-o", "-", path).Output()
	if err != nil {
		t.Fatalf("plutil -convert: %v", err)
	}
	if string(canonical) != ours {
		t.Errorf("Apple's canonical form differs from ours.\nours:\n%s\nplutil:\n%s", ours, canonical)
	}
}

// FuzzStringsSurviveAnXMLParser: whatever string goes in, the output is either
// refused or well-formed XML that decodes back to exactly that string. This is
// the property escape exists to uphold, and the one a hand-written escaper
// most easily breaks.
func FuzzStringsSurviveAnXMLParser(f *testing.F) {
	for _, s := range []string{"", "plain", "a & b", "<>&", "]]>", "tab\t", "é日本😀", "a\x00b", "\xff", "￾"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		// XML parsers fold "\r\n" and a bare "\r" to "\n" before the
		// application sees the text, so those cannot round-trip by design.
		if strings.ContainsRune(s, '\r') {
			t.Skip()
		}
		out, err := plist.Marshal(map[string]any{"k": s})
		if err != nil {
			return
		}
		dec := xml.NewDecoder(strings.NewReader(string(out)))
		var texts []string
		var in string
		for {
			tok, err := dec.Token()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("output is not well-formed XML for %q: %v\n%s", s, err, out)
			}
			switch tt := tok.(type) {
			case xml.StartElement:
				in = tt.Name.Local
			case xml.CharData:
				if in == "string" {
					texts = append(texts, string(tt))
				}
			case xml.EndElement:
				in = ""
			}
		}
		if got := strings.Join(texts, ""); got != s {
			t.Fatalf("decoded %q, want %q", got, s)
		}
	})
}
