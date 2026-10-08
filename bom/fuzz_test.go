package bom_test

import (
	"bytes"
	"encoding/binary"
	"slices"
	"testing"

	"github.com/TotallyGamerJet/macbundle/bom"
)

// seedContainers returns a few valid containers for the fuzzer to mutate.
// Starting from structurally valid input is what lets it reach the tree and
// variable code, which random bytes almost never get past the header to touch.
func seedContainers(tb testing.TB) [][]byte {
	tb.Helper()
	var out [][]byte

	add := func(build func(*bom.Writer)) {
		w := bom.NewWriter()
		build(w)
		b, err := w.Bytes()
		if err != nil {
			tb.Fatal(err)
		}
		out = append(out, b)
	}

	add(func(w *bom.Writer) {})
	add(func(w *bom.Writer) {
		id := w.AddBlock([]byte("hello"))
		if err := w.SetVariable("Greeting", id); err != nil {
			tb.Fatal(err)
		}
	})
	add(func(w *bom.Writer) {
		if err := w.AddTree("Empty", bom.DefaultBlockSize, nil); err != nil {
			tb.Fatal(err)
		}
	})
	add(func(w *bom.Writer) {
		if err := w.AddTree("Small", bom.DefaultBlockSize, entries(5)); err != nil {
			tb.Fatal(err)
		}
	})
	// A page of 40 bytes holds three pairs, so 50 entries force several leaves
	// and more than one internal level.
	add(func(w *bom.Writer) {
		if err := w.AddTree("Deep", 40, entries(50)); err != nil {
			tb.Fatal(err)
		}
	})
	return out
}

// FuzzParse feeds arbitrary bytes to the reader and then asks for everything it
// can be asked for. The only property is that none of it panics or fails to
// terminate: a build tool that reads a file it was handed must reject a bad one
// rather than crash on it.
func FuzzParse(f *testing.F) {
	for _, seed := range seedContainers(f) {
		f.Add(seed)
	}
	f.Add([]byte("BOMStore"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := bom.Parse(data)
		if err != nil {
			return
		}
		_ = r.NumBlocks()
		for slot := range r.TableLen() {
			_, _ = r.Block(bom.BlockID(slot))
		}
		for _, name := range r.Variables() {
			_, _ = r.Variable(name)
			_, _ = r.VariableBlock(name)
			_, _ = r.Tree(name)
		}
	})
}

// FuzzTreeRoundTrip checks the writer rather than the reader: any set of
// distinct keys must come back from Parse, in key order, with the values they
// were given — whatever the page size.
func FuzzTreeRoundTrip(f *testing.F) {
	f.Add([]byte("a\x00b\x00c"), uint8(0))
	f.Add([]byte("a\x00b\x00c"), uint8(5))
	f.Add([]byte("one\x00two\x00three\x00four\x00five\x00six"), uint8(3))
	f.Add([]byte{}, uint8(1))

	f.Fuzz(func(t *testing.T, raw []byte, pageSel uint8) {
		// Keys are the NUL-separated fields of raw, de-duplicated; each value is
		// derived from its key so a mix-up between them is visible.
		var want []bom.TreeEntry
		seen := map[string]bool{}
		for _, key := range bytes.Split(raw, []byte{0}) {
			if seen[string(key)] {
				continue
			}
			seen[string(key)] = true
			want = append(want, bom.TreeEntry{
				Key:   key,
				Value: append([]byte("v:"), key...),
			})
		}
		slices.SortFunc(want, func(a, b bom.TreeEntry) int { return bytes.Compare(a.Key, b.Key) })

		// Page sizes from the smallest that holds two pairs up to the largest
		// whose entry count still fits the format's 16 bits.
		sizes := []uint32{minPage, 36, 64, 512, bom.DefaultBlockSize, maxPage}
		size := sizes[int(pageSel)%len(sizes)]

		w := bom.NewWriter()
		if err := w.AddTree("T", size, want); err != nil {
			t.Fatalf("AddTree: %v", err)
		}
		b, err := w.Bytes()
		if err != nil {
			t.Fatalf("Bytes: %v", err)
		}
		r, err := bom.Parse(b)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		got, err := r.Tree("T")
		if err != nil {
			t.Fatalf("Tree: %v", err)
		}
		if len(got) != len(want) {
			t.Fatalf("got %d entries, want %d", len(got), len(want))
		}
		for i := range want {
			if !bytes.Equal(got[i].Key, want[i].Key) || !bytes.Equal(got[i].Value, want[i].Value) {
				t.Fatalf("entry %d: got %q=%q, want %q=%q",
					i, got[i].Key, got[i].Value, want[i].Key, want[i].Value)
			}
		}
	})
}

// TestParseRejectsOutOfRangeCounts pins the cases a reader that trusts its
// header gets wrong: a table or variable list claiming more entries than the
// file could hold.
func TestParseRejectsOutOfRangeCounts(t *testing.T) {
	good := seedContainers(t)[1]

	// The block table's entry count is the first word at IndexOffset.
	indexOffset := binary.BigEndian.Uint32(good[16:])
	varsOffset := binary.BigEndian.Uint32(good[24:])

	for name, mutate := range map[string]func([]byte){
		"table count":    func(b []byte) { binary.BigEndian.PutUint32(b[indexOffset:], 0xFFFFFFFF) },
		"variable count": func(b []byte) { binary.BigEndian.PutUint32(b[varsOffset:], 0xFFFFFFFF) },
		"index offset":   func(b []byte) { binary.BigEndian.PutUint32(b[16:], 0xFFFFFFFF) },
		"vars offset":    func(b []byte) { binary.BigEndian.PutUint32(b[24:], 0xFFFFFFFF) },
	} {
		t.Run(name, func(t *testing.T) {
			b := bytes.Clone(good)
			mutate(b)
			if _, err := bom.Parse(b); err == nil {
				t.Error("a container with an impossible count was accepted")
			}
		})
	}
}
