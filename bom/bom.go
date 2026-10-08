// Package bom reads and writes Apple's Bill of Materials container.
//
// A BOM file is a flat store of numbered byte blocks, a handful of named
// variables pointing at some of those blocks, and B-trees built out of them.
// Apple uses it for two quite different things: the receipt that records what a
// .pkg installed, and the compiled asset catalog (Assets.car) that holds an
// application's images. This package is the container; package assetcatalog
// puts CoreUI structures inside one.
//
// The tools that normally produce these, mkbom and actool, ship with macOS and
// Xcode. This package needs neither, so a build can run anywhere a Go toolchain
// does.
//
// # Format
//
// Everything in the container is big-endian, which is worth stating because the
// CoreUI structures stored *inside* an asset catalog are little-endian. Getting
// that boundary wrong produces a file that parses and means nothing.
//
//	offset 0    BOM header: magic, version, block count, and the location of
//	            the block table and the variable list
//	offset 512  block data, back to back
//	            the variable list
//	            the block table, then the free list
//
// The header is 32 bytes and the first block starts at 512; the space between
// is unused, which is what Apple's own writer does.
//
// Every structure was read out of files macOS already ships: a receipt from
// /var/db/receipts and an Assets.car from a system framework. The field widths
// are what those files contain, not what a header file suggests. Two are easy
// to get wrong: a tree node's fixed part is twelve bytes rather than eight, and
// a leaf's pairs are (value, key) rather than (key, value).
package bom

import (
	"encoding/binary"
	"fmt"
)

// magic is the first eight bytes of every BOM file.
const magic = "BOMStore"

// version is the only container version Apple has ever written.
const version uint32 = 1

// headerSize is the size of the fixed header, and blockDataStart is where the
// first block may begin. Apple leaves the gap between them empty.
const (
	headerSize     = 32
	blockDataStart = 512
)

// DefaultBlockSize is the tree page size Apple uses for most trees. A page
// holds (DefaultBlockSize-nodeHeaderSize)/pairSize entries, which is 510.
const DefaultBlockSize uint32 = 4096

// nodeHeaderSize is the fixed part of a tree node: isLeaf and count as 16-bit
// values, then the forward and backward links as 32-bit ones.
//
// Twelve, not eight. Reading it as eight shifts every pair by four bytes, which
// still parses — the first entry simply comes out pointing at the null block —
// so it is the kind of mistake that survives a casual test.
const nodeHeaderSize = 12

// pairSize is one entry in a tree node: two block numbers.
const pairSize = 8

// BlockID identifies a block within the container. Zero is the null block: it
// is always present, always empty, and is what an absent reference points at.
type BlockID uint32

// NullBlock is the reserved empty block every BOM file begins its table with.
const NullBlock BlockID = 0

// treeMagic marks a block that describes a tree rather than holding data.
const treeMagic = "tree"

// treeHeaderMinSize is the part of a tree header every writer produces: the
// magic, a version, the root node's block, the page size, the number of entries
// in the whole tree, and a flag byte.
//
// treeHeaderSize is what this package writes, and it is longer. mkbom stops at
// 21 bytes and the readers of receipts are content with that, but CoreUI reads
// 29 and a 21-byte header makes it walk off the end of the block —
// "BOMStreamGetDataPointer buffer overflow", which is how this was found. The
// extra eight bytes are a key size and a reserved word.
//
// Writing the longer form is safe in both directions: a reader that wants 21
// bytes ignores the rest, and a reader that wants 29 gets them.
const (
	treeHeaderMinSize = 21
	treeHeaderSize    = 29
)

// TreeEntry is one key/value pair in a tree. Both are opaque to this package:
// what they mean is decided by whoever is reading the tree, which for an asset
// catalog is CoreUI.
type TreeEntry struct {
	Key   []byte
	Value []byte
}

// header is the fixed part of the file.
type header struct {
	version     uint32
	NumBlocks   uint32
	IndexOffset uint32
	IndexLength uint32
	VarsOffset  uint32
	VarsLength  uint32
}

func (h header) marshal() []byte {
	b := make([]byte, headerSize)
	copy(b, magic)
	binary.BigEndian.PutUint32(b[8:], h.version)
	binary.BigEndian.PutUint32(b[12:], h.NumBlocks)
	binary.BigEndian.PutUint32(b[16:], h.IndexOffset)
	binary.BigEndian.PutUint32(b[20:], h.IndexLength)
	binary.BigEndian.PutUint32(b[24:], h.VarsOffset)
	binary.BigEndian.PutUint32(b[28:], h.VarsLength)
	return b
}

func parseHeader(b []byte) (header, error) {
	if len(b) < headerSize {
		return header{}, fmt.Errorf("bom: file is %d bytes, too short for a header", len(b))
	}
	if string(b[:8]) != magic {
		return header{}, fmt.Errorf("bom: not a BOM file: magic is %q, want %q", b[:8], magic)
	}
	return header{
		version:     binary.BigEndian.Uint32(b[8:]),
		NumBlocks:   binary.BigEndian.Uint32(b[12:]),
		IndexOffset: binary.BigEndian.Uint32(b[16:]),
		IndexLength: binary.BigEndian.Uint32(b[20:]),
		VarsOffset:  binary.BigEndian.Uint32(b[24:]),
		VarsLength:  binary.BigEndian.Uint32(b[28:]),
	}, nil
}

// variable is a name bound to a block.
type variable struct {
	Name  string
	Block BlockID
}
