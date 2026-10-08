package bom

import (
	"encoding/binary"
	"fmt"
)

// Reader gives access to a BOM container that is already in memory.
//
// It exists mostly so the writer can be checked against Apple's own files: a
// format reverse-engineered from a blog post and a header file is worth very
// little until something reads a receipt macOS itself produced and gets the
// same answer lsbom does.
type Reader struct {
	data     []byte
	header   header
	pointers []pointer
	vars     map[string]BlockID
	order    []string
}

type pointer struct{ address, length uint32 }

// Parse reads a container's index. Block contents are not copied; the slices
// returned by Block point into data.
func Parse(data []byte) (*Reader, error) {
	h, err := parseHeader(data)
	if err != nil {
		return nil, err
	}
	r := &Reader{data: data, header: h, vars: make(map[string]BlockID)}

	if err := r.readPointers(); err != nil {
		return nil, err
	}
	if err := r.readVars(); err != nil {
		return nil, err
	}
	return r, nil
}

// Offsets, counts and lengths in the file are 32-bit, and every sum of them is
// done in 64 so that none can wrap: on a 32-bit platform an int sum of two
// plausible-looking values can go negative, pass a "past the end" test, and then
// index with it.

func (r *Reader) readPointers() error {
	size := uint64(len(r.data))
	at := uint64(r.header.IndexOffset)
	if at+4 > size {
		return fmt.Errorf("bom: the block table starts at %#x, past the end of a %d-byte file",
			at, size)
	}
	off := int(at)
	n := binary.BigEndian.Uint32(r.data[off:])
	if at+4+uint64(n)*8 > size {
		return fmt.Errorf("bom: the block table claims %d entries, which do not fit in the file", n)
	}
	r.pointers = make([]pointer, n)
	for i := range r.pointers {
		base := off + 4 + i*8
		r.pointers[i] = pointer{
			address: binary.BigEndian.Uint32(r.data[base:]),
			length:  binary.BigEndian.Uint32(r.data[base+4:]),
		}
	}
	return nil
}

func (r *Reader) readVars() error {
	at := uint64(r.header.VarsOffset)
	if at+4 > uint64(len(r.data)) {
		return fmt.Errorf("bom: the variable list starts at %#x, past the end of the file", at)
	}
	off := int(at)
	n := binary.BigEndian.Uint32(r.data[off:])
	off += 4
	for range n {
		if off+5 > len(r.data) {
			return fmt.Errorf("bom: the variable list is truncated")
		}
		id := BlockID(binary.BigEndian.Uint32(r.data[off:]))
		length := int(r.data[off+4])
		off += 5
		if off+length > len(r.data) {
			return fmt.Errorf("bom: a variable name runs past the end of the file")
		}
		name := string(r.data[off : off+length])
		off += length
		r.vars[name] = id
		r.order = append(r.order, name)
	}
	return nil
}

// NumBlocks is the block count the header records, which is the number of
// blocks that hold something.
func (r *Reader) NumBlocks() uint32 { return r.header.NumBlocks }

// TableLen is the number of slots in the block table, which is not the same
// thing: Apple's writer over-allocates the table and leaves most of it null, so
// a receipt with 22 blocks routinely has a table of 2730 slots. Anything
// enumerating blocks has to walk the slots, because a tree's references are
// slot numbers.
func (r *Reader) TableLen() int { return len(r.pointers) }

// Variables lists the variable names, in the order the file stores them.
func (r *Reader) Variables() []string { return append([]string(nil), r.order...) }

// Block returns a block's contents.
func (r *Reader) Block(id BlockID) ([]byte, error) {
	if int(id) >= len(r.pointers) {
		return nil, fmt.Errorf("bom: block %d is beyond the %d in the table", id, len(r.pointers))
	}
	p := r.pointers[id]
	if p.address == 0 && p.length == 0 {
		return nil, nil
	}
	end := uint64(p.address) + uint64(p.length)
	if end > uint64(len(r.data)) {
		return nil, fmt.Errorf("bom: block %d runs from %#x to %#x, past the end of the file",
			id, p.address, end)
	}
	return r.data[p.address:end], nil
}

// Variable returns the block a name is bound to.
func (r *Reader) Variable(name string) (BlockID, error) {
	id, ok := r.vars[name]
	if !ok {
		return 0, fmt.Errorf("bom: no variable named %q", name)
	}
	return id, nil
}

// VariableBlock returns the contents of the block a name is bound to.
func (r *Reader) VariableBlock(name string) ([]byte, error) {
	id, err := r.Variable(name)
	if err != nil {
		return nil, err
	}
	return r.Block(id)
}

// Tree walks the tree bound to a name and returns its entries in key order.
//
// Only the leaves are read. The internal nodes exist so that a reader looking
// for one key does not have to, and following the leaves' forward links is
// both simpler and the order the entries are meant to be in.
func (r *Reader) Tree(name string) ([]TreeEntry, error) {
	head, err := r.VariableBlock(name)
	if err != nil {
		return nil, err
	}
	// The minimum, not what this package writes: receipts produced by mkbom
	// carry the shorter header and are perfectly readable.
	if len(head) < treeHeaderMinSize {
		return nil, fmt.Errorf("bom: %q is %d bytes, too short for a tree header", name, len(head))
	}
	if string(head[:4]) != treeMagic {
		return nil, fmt.Errorf("bom: %q is not a tree: magic is %q", name, head[:4])
	}
	root := BlockID(binary.BigEndian.Uint32(head[8:]))
	want := int(binary.BigEndian.Uint32(head[16:]))

	// Descend to the leftmost leaf, then follow the forward links.
	seen := make(map[BlockID]bool)
	for {
		leaf, pairs, err := r.node(root)
		if err != nil {
			return nil, fmt.Errorf("bom: tree %q: %w", name, err)
		}
		if leaf {
			break
		}
		if len(pairs) == 0 {
			return nil, fmt.Errorf("bom: tree %q has an internal node with no children", name)
		}
		if seen[root] {
			return nil, fmt.Errorf("bom: tree %q loops on block %d", name, root)
		}
		seen[root] = true
		root = pairs[0]
	}

	var out []TreeEntry
	visited := make(map[BlockID]bool)
	for id := root; id != NullBlock; {
		if visited[id] {
			return nil, fmt.Errorf("bom: tree %q loops through leaf %d", name, id)
		}
		visited[id] = true

		block, err := r.Block(id)
		if err != nil {
			return nil, err
		}
		_, pairs, err := r.node(id)
		if err != nil {
			return nil, fmt.Errorf("bom: tree %q: %w", name, err)
		}
		for i := 0; i+1 < len(pairs); i += 2 {
			value, err := r.Block(pairs[i])
			if err != nil {
				return nil, err
			}
			key, err := r.Block(pairs[i+1])
			if err != nil {
				return nil, err
			}
			out = append(out, TreeEntry{Key: key, Value: value})
		}
		id = BlockID(binary.BigEndian.Uint32(block[4:]))
	}

	if len(out) != want {
		return nil, fmt.Errorf("bom: tree %q says it holds %d entries but its leaves hold %d",
			name, want, len(out))
	}
	return out, nil
}

// node decodes one tree page into its flag and its flattened block numbers.
func (r *Reader) node(id BlockID) (leaf bool, pairs []BlockID, err error) {
	b, err := r.Block(id)
	if err != nil {
		return false, nil, err
	}
	if len(b) < nodeHeaderSize {
		return false, nil, fmt.Errorf("node %d is %d bytes, too short for a header", id, len(b))
	}
	leaf = binary.BigEndian.Uint16(b) == 1
	count := int(binary.BigEndian.Uint16(b[2:]))
	if nodeHeaderSize+count*pairSize > len(b) {
		return false, nil, fmt.Errorf("node %d claims %d entries, which do not fit in %d bytes",
			id, count, len(b))
	}
	pairs = make([]BlockID, 0, count*2)
	for i := range count {
		base := nodeHeaderSize + i*pairSize
		pairs = append(pairs,
			BlockID(binary.BigEndian.Uint32(b[base:])),
			BlockID(binary.BigEndian.Uint32(b[base+4:])))
	}
	return leaf, pairs, nil
}
