package bom

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
)

// Writer assembles a BOM container.
//
// Blocks are added, some of them are named, and WriteTo lays the whole thing
// out. Nothing is written until then, because a block's address is not known
// until every block's size is.
//
// A Writer is not safe for concurrent use; a build step has no reason to want
// that.
type Writer struct {
	// blocks[0] is the null block and is never written to.
	blocks [][]byte
	vars   []variable
}

// NewWriter returns an empty container holding only the null block.
func NewWriter() *Writer {
	return &Writer{blocks: [][]byte{nil}}
}

// AddBlock stores a block and returns its number.
//
// The slice is retained rather than copied: this is a build step assembling
// buffers it has just produced, and copying every rendition of an asset catalog
// to guard against a caller that mutates its own input would cost more than it
// is worth. Do not modify a slice after adding it.
func (w *Writer) AddBlock(data []byte) BlockID {
	w.blocks = append(w.blocks, data)
	return BlockID(len(w.blocks) - 1)
}

// reserve allocates a block number whose contents are filled in later.
//
// Tree nodes need this: a leaf records the block numbers of the leaves either
// side of it, so the numbers have to exist before any of the nodes can be
// encoded.
func (w *Writer) reserve() BlockID {
	return w.AddBlock(nil)
}

func (w *Writer) set(id BlockID, data []byte) {
	w.blocks[id] = data
}

// SetVariable binds a name to a block, replacing any previous binding.
//
// The name is length-prefixed with a single byte in the file, so it cannot
// exceed 255 bytes. Every name Apple uses is a short ASCII constant.
func (w *Writer) SetVariable(name string, id BlockID) error {
	if name == "" {
		return fmt.Errorf("bom: a variable needs a name")
	}
	if len(name) > 255 {
		return fmt.Errorf("bom: variable name %q is %d bytes, the format allows 255", name, len(name))
	}
	if int(id) >= len(w.blocks) {
		return fmt.Errorf("bom: variable %q refers to block %d, which does not exist", name, id)
	}
	for i := range w.vars {
		if w.vars[i].Name == name {
			w.vars[i].Block = id
			return nil
		}
	}
	w.vars = append(w.vars, variable{Name: name, Block: id})
	return nil
}

// AddTree builds a B-tree from the given entries and binds it to a name.
//
// Entries are sorted by key, bytewise, because that is the order Apple's own
// files are in and the order a reader doing a binary search would require. The
// caller's slice is not modified.
//
// blockSize is the page size; pass DefaultBlockSize unless a particular
// consumer wants something else. CoreUI, for instance, pages its bitmap-key
// tree at 1024.
func (w *Writer) AddTree(name string, blockSize uint32, entries []TreeEntry) error {
	if blockSize < nodeHeaderSize+pairSize {
		return fmt.Errorf("bom: block size %d is too small to hold a single entry", blockSize)
	}

	sorted := make([]TreeEntry, len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool {
		return bytes.Compare(sorted[i].Key, sorted[j].Key) < 0
	})
	for i := 1; i < len(sorted); i++ {
		if bytes.Equal(sorted[i-1].Key, sorted[i].Key) {
			return fmt.Errorf("bom: tree %q has two entries with the same key %x", name, sorted[i].Key)
		}
	}

	keySize := commonKeySize(sorted)
	root, err := w.buildTree(sorted, blockSize, keySize)
	if err != nil {
		return fmt.Errorf("bom: building tree %q: %w", name, err)
	}

	head := make([]byte, treeHeaderSize)
	copy(head, treeMagic)
	binary.BigEndian.PutUint32(head[4:], version)
	binary.BigEndian.PutUint32(head[8:], uint32(root))
	binary.BigEndian.PutUint32(head[12:], blockSize)
	binary.BigEndian.PutUint32(head[16:], uint32(len(sorted)))
	// head[20] is a flag byte. Apple leaves it zero for the trees a catalog is
	// read through and sets it for its bitmap-key tree; nothing is known to
	// depend on it, so it stays zero.
	binary.BigEndian.PutUint32(head[21:], keySize)
	// head[25:29] is reserved and always zero.

	return w.SetVariable(name, w.AddBlock(head))
}

// commonKeySize is the width every key in the tree shares, or zero if they
// differ.
//
// Apple's own catalogs record it: 18 for the rendition tree, whose keys are
// nine 16-bit attributes, and 7 for a facet tree holding the single name
// "AppIcon". Both are consistent with "the fixed key width, or nothing if there
// is not one", which is what this computes — with only one entry in the
// reference there was no way to tell that from "the largest key", so the
// conservative reading is the one that reports nothing when they disagree.
func commonKeySize(entries []TreeEntry) uint32 {
	if len(entries) == 0 {
		return 0
	}
	size := len(entries[0].Key)
	for _, e := range entries[1:] {
		if len(e.Key) != size {
			return 0
		}
	}
	return uint32(size)
}

// node is one tree page while it is being built.
type node struct {
	id BlockID
	// lastKey is the greatest key beneath this node. An internal node stores
	// it as the separator for the child, which is how Apple's own trees are
	// laid out: the pair is (child, last key in that child).
	lastKey []byte
}

// buildTree lays out leaves and then as many internal levels as it takes to
// reach a single root.
func (w *Writer) buildTree(entries []TreeEntry, blockSize, keySize uint32) (BlockID, error) {
	perPage := int((blockSize - nodeHeaderSize) / pairSize)

	// An empty tree still needs a root, or a reader following the header has
	// nowhere to go. Apple writes an empty leaf; so do we.
	if len(entries) == 0 {
		id := w.reserve()
		w.set(id, encodeNode(true, nil, nil, NullBlock, NullBlock, blockSize, keySize))
		return id, nil
	}

	// Keys and values become blocks of their own; the leaf holds only numbers.
	type pair struct{ value, key BlockID }
	pairs := make([]pair, len(entries))
	for i, e := range entries {
		pairs[i] = pair{value: w.AddBlock(e.Value), key: w.AddBlock(e.Key)}
	}

	var leaves []node
	for start := 0; start < len(pairs); start += perPage {
		end := min(start+perPage, len(pairs))
		leaves = append(leaves, node{
			id:      w.reserve(),
			lastKey: entries[end-1].Key,
		})
	}

	// Now that every leaf has a number, the links can be filled in.
	for i, start := 0, 0; i < len(leaves); i, start = i+1, start+perPage {
		end := min(start+perPage, len(pairs))
		var fwd, bwd BlockID
		if i+1 < len(leaves) {
			fwd = leaves[i+1].id
		}
		if i > 0 {
			bwd = leaves[i-1].id
		}
		flat := make([]BlockID, 0, 2*(end-start))
		keys := make([][]byte, 0, end-start)
		for j, p := range pairs[start:end] {
			flat = append(flat, p.value, p.key)
			keys = append(keys, entries[start+j].Key)
		}
		w.set(leaves[i].id, encodeNode(true, flat, keys, fwd, bwd, blockSize, keySize))
	}

	// Internal levels, until one node is left. Each pair is (child, the
	// greatest key beneath that child).
	level := leaves
	for len(level) > 1 {
		var next []node
		for start := 0; start < len(level); start += perPage {
			end := min(start+perPage, len(level))
			children := level[start:end]

			flat := make([]BlockID, 0, 2*len(children))
			keys := make([][]byte, 0, len(children))
			for _, c := range children {
				flat = append(flat, c.id, w.AddBlock(c.lastKey))
				keys = append(keys, c.lastKey)
			}
			id := w.reserve()
			w.set(id, encodeNode(false, flat, keys, NullBlock, NullBlock, blockSize, keySize))
			next = append(next, node{id: id, lastKey: children[len(children)-1].lastKey})
		}
		if len(next) >= len(level) {
			// Cannot happen with perPage >= 2, and would loop forever if it
			// did. Refusing is better than hanging a build.
			return 0, fmt.Errorf("a page holds %d children, which cannot reduce %d nodes",
				perPage, len(level))
		}
		level = next
	}
	return level[0].id, nil
}

// encodeNode lays out one tree page.
//
// flat is the pairs already flattened — (value, key) for a leaf, (child,
// separator key) for an internal node — and keys is the key bytes those pairs
// are addressed by.
//
// Three things about the layout are not obvious and each was found by a file
// being refused or misread:
//
//   - the block is the full page size however few entries it holds, because
//     CoreUI reads a whole page. A block sized to its contents makes it read
//     past the end and refuse the file outright, with
//     "BOMStreamGetDataPointer buffer overflow";
//   - the keys are written again *inside* the node, after the pairs, and that
//     is what CoreUI binary-searches. Leaving the region zero does not fail:
//     every comparison matches, the search lands in the middle of the array,
//     and every lookup returns the same entry. A catalog of ten icons read back
//     as the same icon ten times;
//   - the inline keys start four bytes after the pairs end, and the block is
//     that much longer than a page as a result. Apple's own catalogs are laid
//     out the same way, which is where the offset came from.
func encodeNode(leaf bool, flat []BlockID, keys [][]byte, forward, backward BlockID, blockSize, keySize uint32) []byte {
	count := len(flat) / 2
	size := max(int(blockSize), nodeHeaderSize+pairSize*count)
	size += count * int(keySize)
	b := make([]byte, size)
	if leaf {
		binary.BigEndian.PutUint16(b[0:], 1)
	}
	binary.BigEndian.PutUint16(b[2:], uint16(count))
	binary.BigEndian.PutUint32(b[4:], uint32(forward))
	binary.BigEndian.PutUint32(b[8:], uint32(backward))
	for i, id := range flat {
		binary.BigEndian.PutUint32(b[nodeHeaderSize+4*i:], uint32(id))
	}

	// The keys again, where CoreUI looks for them.
	if keySize > 0 {
		at := nodeHeaderSize + pairSize*count + inlineKeyGap
		for _, k := range keys {
			if len(k) != int(keySize) || at+len(k) > len(b) {
				// Only a tree whose keys are all one width has an inline area
				// at all; commonKeySize reports zero otherwise and this is
				// skipped entirely.
				break
			}
			copy(b[at:], k)
			at += int(keySize)
		}
	}
	return b
}

// inlineKeyGap is the space between the end of the pair array and the first
// inline key. Four bytes, reserved and always zero in Apple's files.
const inlineKeyGap = 4

// Bytes lays the container out and returns it.
func (w *Writer) Bytes() ([]byte, error) {
	var buf bytes.Buffer
	if _, err := w.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// WriteTo lays the container out and writes it.
//
// The order is header, block data, variables, then the index — the block table
// followed by the free list. Only the header's offsets tie them together, so
// the order is ours to choose; this one keeps the variable-length parts at the
// end where their sizes are known last.
func (w *Writer) WriteTo(out io.Writer) (int64, error) {
	var body bytes.Buffer
	body.Write(make([]byte, blockDataStart-headerSize))

	type pointer struct{ address, length uint32 }
	pointers := make([]pointer, len(w.blocks))

	for i, b := range w.blocks {
		if i == int(NullBlock) {
			continue // address 0, length 0
		}
		pointers[i] = pointer{
			address: uint32(blockDataStart + body.Len() - (blockDataStart - headerSize)),
			length:  uint32(len(b)),
		}
		body.Write(b)
	}

	// The variable list and the index are appended by hand rather than through
	// binary.Write. Writing to a bytes.Buffer cannot fail, so every call would
	// return an error that exists only to be discarded — and this project does
	// not discard errors. Appending to a slice creates none to begin with.
	varsOffset := uint32(headerSize + body.Len())
	vars := be32(nil, uint32(len(w.vars)))
	for _, v := range w.vars {
		vars = be32(vars, uint32(v.Block))
		vars = append(vars, byte(len(v.Name)))
		vars = append(vars, v.Name...)
	}
	varsLength := uint32(len(vars))
	body.Write(vars)

	indexOffset := uint32(headerSize + body.Len())
	index := be32(nil, uint32(len(pointers)))
	for _, p := range pointers {
		index = be32(index, p.address)
		index = be32(index, p.length)
	}
	// The free list records slack a rewriter could reuse. We lay every block
	// out back to back and never rewrite in place, so there is none.
	index = be32(index, 0)
	indexLength := uint32(len(index))
	body.Write(index)

	h := header{
		version: version,
		// The count is of real blocks: the null block is present in the table
		// but is not one of them.
		NumBlocks:   uint32(len(w.blocks) - 1),
		IndexOffset: indexOffset,
		IndexLength: indexLength,
		VarsOffset:  varsOffset,
		VarsLength:  varsLength,
	}

	n, err := out.Write(h.marshal())
	if err != nil {
		return int64(n), fmt.Errorf("bom: writing the header: %w", err)
	}
	m, err := out.Write(body.Bytes())
	if err != nil {
		return int64(n + m), fmt.Errorf("bom: writing the body: %w", err)
	}
	return int64(n + m), nil
}

// be32 appends a big-endian 32-bit value.
func be32(b []byte, v uint32) []byte {
	return binary.BigEndian.AppendUint32(b, v)
}
