package bom_test

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/TotallyGamerJet/darwinbundle/bom"
)

// entries builds n distinct key/value pairs, large enough to page a tree when
// asked for.
func entries(n int) []bom.TreeEntry {
	out := make([]bom.TreeEntry, n)
	for i := range out {
		out[i] = bom.TreeEntry{
			Key:   fmt.Appendf(nil, "key-%06d", i),
			Value: fmt.Appendf(nil, "value-%06d", i),
		}
	}
	return out
}

func write(t *testing.T, build func(*bom.Writer)) *bom.Reader {
	t.Helper()
	w := bom.NewWriter()
	build(w)
	data, err := w.Bytes()
	if err != nil {
		t.Fatalf("writing: %v", err)
	}
	r, err := bom.Parse(data)
	if err != nil {
		t.Fatalf("parsing what we just wrote: %v", err)
	}
	return r
}

func TestBlocksRoundTrip(t *testing.T) {
	var ids []bom.BlockID
	want := [][]byte{[]byte("first"), {}, bytes.Repeat([]byte{0xAB}, 5000)}

	r := write(t, func(w *bom.Writer) {
		for _, b := range want {
			ids = append(ids, w.AddBlock(b))
		}
	})

	for i, id := range ids {
		got, err := r.Block(id)
		if err != nil {
			t.Fatalf("Block(%d): %v", id, err)
		}
		if !bytes.Equal(got, want[i]) {
			t.Errorf("block %d came back as %d bytes, want %d", id, len(got), len(want[i]))
		}
	}

	// The null block is always present and always empty, which is what an
	// absent reference points at.
	if got, err := r.Block(bom.NullBlock); err != nil || len(got) != 0 {
		t.Errorf("the null block came back as %v (%v), want empty", got, err)
	}
	if got := r.NumBlocks(); got != uint32(len(want)) {
		t.Errorf("the header counts %d blocks, want %d — the null block is not one of them",
			got, len(want))
	}
}

func TestVariablesRoundTrip(t *testing.T) {
	r := write(t, func(w *bom.Writer) {
		a := w.AddBlock([]byte("alpha"))
		b := w.AddBlock([]byte("beta"))
		if err := w.SetVariable("First", a); err != nil {
			t.Fatal(err)
		}
		if err := w.SetVariable("Second", b); err != nil {
			t.Fatal(err)
		}
		// Rebinding replaces rather than duplicating.
		if err := w.SetVariable("First", b); err != nil {
			t.Fatal(err)
		}
	})

	if got := r.Variables(); len(got) != 2 {
		t.Fatalf("variables are %v, want two of them", got)
	}
	got, err := r.VariableBlock("First")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "beta" {
		t.Errorf("First resolves to %q, want the block it was rebound to", got)
	}
}

func TestSetVariableRejectsWhatTheFormatCannotHold(t *testing.T) {
	w := bom.NewWriter()
	id := w.AddBlock([]byte("x"))

	if err := w.SetVariable("", id); err == nil {
		t.Error("an empty variable name was accepted")
	}
	// The name is length-prefixed with a single byte.
	if err := w.SetVariable(string(bytes.Repeat([]byte("n"), 256)), id); err == nil {
		t.Error("a 256-byte variable name was accepted; the format prefixes with one byte")
	}
	if err := w.SetVariable("Missing", bom.BlockID(9999)); err == nil {
		t.Error("a variable was bound to a block that does not exist")
	}
}

// A tree small enough for one page is the common case: an asset catalog holds a
// handful of renditions.
func TestSmallTreeRoundTrips(t *testing.T) {
	want := entries(5)
	r := write(t, func(w *bom.Writer) {
		if err := w.AddTree("Small", bom.DefaultBlockSize, want); err != nil {
			t.Fatal(err)
		}
	})

	got, err := r.Tree("Small")
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i := range got {
		if !bytes.Equal(got[i].Key, want[i].Key) || !bytes.Equal(got[i].Value, want[i].Value) {
			t.Errorf("entry %d is %q=%q, want %q=%q",
				i, got[i].Key, got[i].Value, want[i].Key, want[i].Value)
		}
	}
}

// TestATreeThatNeedsSeveralLevelsRoundTrips.
//
// The page size is the smallest one that can hold two entries, so 400 of them
// force leaves, an internal level, and then another above it. Getting the
// separator keys or the leaf links wrong shows up here and nowhere else — a
// single-page tree exercises none of it.
func TestATreeThatNeedsSeveralLevelsRoundTrips(t *testing.T) {
	want := entries(400)
	const tiny = 28 // 12-byte header plus two 8-byte pairs

	r := write(t, func(w *bom.Writer) {
		if err := w.AddTree("Deep", tiny, want); err != nil {
			t.Fatal(err)
		}
	})

	got, err := r.Tree("Deep")
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i := range got {
		if !bytes.Equal(got[i].Key, want[i].Key) {
			t.Fatalf("entry %d has key %q, want %q — the leaves are out of order",
				i, got[i].Key, want[i].Key)
		}
		if !bytes.Equal(got[i].Value, want[i].Value) {
			t.Errorf("entry %d has value %q, want %q", i, got[i].Value, want[i].Value)
		}
	}
}

// Entries come back in key order however they went in: a reader doing a binary
// search over the leaves depends on it, and Apple's own files are sorted.
func TestTreeEntriesAreSortedByKey(t *testing.T) {
	unsorted := []bom.TreeEntry{
		{Key: []byte("zebra"), Value: []byte("3")},
		{Key: []byte("apple"), Value: []byte("1")},
		{Key: []byte("mango"), Value: []byte("2")},
	}
	r := write(t, func(w *bom.Writer) {
		if err := w.AddTree("Sorted", bom.DefaultBlockSize, unsorted); err != nil {
			t.Fatal(err)
		}
	})
	got, err := r.Tree("Sorted")
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, e := range got {
		keys = append(keys, string(e.Key))
	}
	if want := []string{"apple", "mango", "zebra"}; fmt.Sprint(keys) != fmt.Sprint(want) {
		t.Errorf("keys came back as %v, want %v", keys, want)
	}
}

func TestAnEmptyTreeIsStillATree(t *testing.T) {
	r := write(t, func(w *bom.Writer) {
		if err := w.AddTree("Empty", bom.DefaultBlockSize, nil); err != nil {
			t.Fatal(err)
		}
	})
	got, err := r.Tree("Empty")
	if err != nil {
		t.Fatalf("an empty tree could not be read back: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("an empty tree yielded %d entries", len(got))
	}
}

// Two entries under one key would make the tree ambiguous, and a reader doing a
// binary search would find whichever it happened to land on.
func TestDuplicateKeysAreRefused(t *testing.T) {
	w := bom.NewWriter()
	err := w.AddTree("Dup", bom.DefaultBlockSize, []bom.TreeEntry{
		{Key: []byte("same"), Value: []byte("a")},
		{Key: []byte("same"), Value: []byte("b")},
	})
	if err == nil {
		t.Error("a tree with two entries under one key was accepted")
	}
}

func TestGarbageIsRejected(t *testing.T) {
	for name, data := range map[string][]byte{
		"empty": nil,
		"short": []byte("BOM"),
		"wrong magic": append([]byte("NOTABOM!"),
			bytes.Repeat([]byte{0}, 64)...),
	} {
		if _, err := bom.Parse(data); err == nil {
			t.Errorf("%s was accepted as a BOM file", name)
		}
	}
}

// The smallest and largest page AddTree accepts: a 12-byte node header plus two
// 8-byte pairs, and the same header plus 65535 pairs, the most a 16-bit count
// can describe.
const (
	minPage = 12 + 2*8
	maxPage = 12 + 65535*8
)

// TestAddTreeRefusesPagesItCannotUse. A page that holds one pair can never
// reduce a level to a single root, and one that holds more than 65535 cannot
// record its own count. Both used to be accepted — the first failed with an
// obscure message after building the leaves, the second wrote a wrapped count
// and read back wrong with no error at all.
func TestAddTreeRefusesPagesItCannotUse(t *testing.T) {
	for _, size := range []uint32{0, 8, 20, minPage - 1, maxPage + 1, 1 << 24} {
		w := bom.NewWriter()
		if err := w.AddTree("T", size, entries(3)); err == nil {
			t.Errorf("block size %d was accepted", size)
		}
	}
	for _, size := range []uint32{minPage, maxPage} {
		w := bom.NewWriter()
		if err := w.AddTree("T", size, entries(3)); err != nil {
			t.Errorf("block size %d was refused: %v", size, err)
		}
	}
}
