# Writing an Assets.car

Notes on the formats behind packages [`bom`](../bom), [`assetcatalog`](../assetcatalog)
and [`appiconset`](../appiconset). Apple documents none of this. Every layout
here was read out of files macOS already ships (a receipt from `/var/db/receipts`,
an `Assets.car` from a system framework, a catalog compiled by `actool`) and
checked against `assetutil` and `lsbom`, not taken from a header file or a blog
post.

The thing to know before you touch any of it: **this format fails silently.** A
catalog can parse, list every image at the right size and scale, pass
`assetutil`, and still show the wrong icon or noise. Structural tests cannot see
that class of bug, which is why the tests also take renditions apart and compare
their decoded pixels with the source image.

## The two layers

```
Assets.car
└── BOM container                 package bom           big-endian
    ├── CARHEADER                 package assetcatalog  little-endian
    ├── KEYFORMAT
    ├── FACETKEYS   (tree)        name -> partial key
    └── RENDITIONS  (tree)        key  -> image (CSI)
```

**The container is big-endian and everything stored in it is little-endian.**
That is not a mistake in either: the archive format predates the structures
CoreUI memory-maps out of it. A four-character code written `CTAR` reads as
`RATC` in a hex dump, and a catalog whose header is byte-swapped parses and means
nothing.

## BOM container

```
offset 0     header: magic "BOMStore", version, block count,
             and where the block table and variable list are
offset 512   block data, back to back
             the variable list
             the block table, then the free list
```

A BOM is numbered blocks, named variables pointing at some of them, and B-trees
built out of both. Block 0 is the null block.

## Symptoms and causes

| Symptom | Cause |
|---|---|
| `assetutil`: `BOMStreamGetDataPointer buffer overflow` | **A tree node must be a whole page**, however few entries it holds. Apple's receipts carry 4096-byte leaves with 406 entries in them; CoreUI reads a page at a time, so a node sized to its contents makes it read off the end. |
| Same error, different cause | **A tree header is 29 bytes, not 21.** `mkbom` writes 21 and readers of receipts are content with that; CoreUI reads 29. The extra eight are a key size and a reserved word. Writing 29 satisfies both. |
| Every lookup returns the same entry; a catalog of ten icons reads back as one icon ten times | **A tree node carries its keys twice.** The pairs address key blocks, and the same key bytes are written again inside the node, four bytes after the pair array ends. That inline copy is what CoreUI binary-searches. Left zero, every comparison matches and the search lands in the middle. The file is otherwise correct. |
| Parses, but every entry is off by four bytes (the first points at the null block) | A tree node's fixed part is **twelve** bytes, not eight. |
| Entries read back swapped | A leaf's pairs are **(value, key)**, not (key, value). |
| Every image present and correct; the app shows the generic bundle icon | **An icon is a set, and the set needs a descriptor**: a rendition with no pixels (part 218, layout 1010, zero dimensions) whose payload lists the point sizes the set holds. IconServices enumerates *that*. Without it, nothing is enumerated and nothing complains. |
| Icon drawn as noise over black | Compression type 2 is **gzip**, not zlib. A zlib stream begins `78 9c`, gzip `1f 8b 08`; CoreUI decodes the wrong one into garbage rather than refusing it. |
| Same, or a crash reading past the payload | The length recorded beside the pixels is the **stored** byte count (compressed or not), not the pixel count. CoreUI reads it as a length to consume. |
| `assetutil` reports `Opaque: true`; the transparent margin renders as black, making the icon look too small for its canvas | **The CELM version has to match the compression.** Surveying every catalog on a system, versions 1 and 3 appear only with chunked payloads and 0 and 2 only with flat ones; every gzip rendition Apple ships is version 0. 2-with-gzip is a pairing Apple never writes, and CoreUI's response is not to refuse it but to treat the rendition as opaque. The CSI header and info list are byte-identical to `actool`'s; the pixels decode correctly. The one visible difference is four bytes in a header that is not about opacity. |
| CoreUI: "a bad pixel format or failure to create an appropriate image provider" for 32-pixel images only | The bytes-per-row entry is `width × 4`. For a 16-pixel image that is 64, which looks like a constant; the reference catalog held a 16-pixel image, and a byte comparison against `actool` kept passing because both files agreed on the wrong-for-any-other-size value. |

## How each claim was checked

- **`bom`**: reads every receipt under `/var/db/receipts` and every system
  `Assets.car` it can; compares paths with `lsbom`; and transcodes a receipt
  block for block and confirms `lsbom` reads the copy identically.
- **`assetcatalog`**: compiles the same artwork with `actool` and compares the
  rendition's header and info list byte for byte, and the whole rendition when
  both stored the pixels the same way (they differ when `actool` chose LZFSE and
  this chose gzip); hands what it wrote to `assetutil`; checks that distinct
  renditions are distinguishable; and decodes pixels back out and compares them.
- **`appiconset`**: `assetutil` must list ten icon images under the facet name.

All of these skip where the tool is absent, so on Linux the structural and
round-trip checks carry the weight, and a macOS CI job runs the rest.

## What is deliberately different from `actool`

- **Compression.** `actool` uses LZFSE, which has no pure-Go encoder worth
  depending on. gzip is in the standard library and CoreUI reads it. A 1024-pixel
  icon is 4 MB raw and tens of kilobytes compressed. Images too small for gzip to
  help are stored whole.
- **No atlas.** `actool` packs small icons into an atlas; every image here is
  stored on its own. Larger on disk, identical on screen.
- **No timestamps.** Nothing in the output depends on the clock, so a rebuild
  does not churn the bundle.
