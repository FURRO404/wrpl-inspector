# oodle

A pure Go decoder for Oodle LZ streams. It reads the four codecs War Thunder
ships: **Kraken**, **Mermaid**, **Selkie** and **Leviathan**.

No cgo. No dependencies outside the standard library. No Oodle DLL.

This is a port of the reverse engineered decoder in
[powzix/ooz](https://github.com/powzix/ooz) (`kraken.cpp`).

## Not supported

LZNA (decoder type 5) and Bitknit (type 11). Both are old Oodle codecs. The
decoder reports `ErrUnsupported` for them instead of failing with a vague error.

## Library

```go
import "github.com/maxsupermanhd/wrpl-inspector/v3/wtcontent/oodle"

// The size of the decoded data must be known in advance. Oodle streams do not
// record it.
data, err := oodle.Decompress(src, decompressedSize)
```

- `Decompress` ignores input bytes after the last quantum the output needs. The
  DDSx texture streams in War Thunder carry such bytes.
- `DecompressStrict` reports an error for those bytes instead.
- If `decompressedSize` is larger than the stream holds, both return
  `ErrShortStream`. The C original cannot notice this. It reads whatever
  follows the literal stream in memory and returns it as data.

## Command

```
go build -o oodle ./cmd/oodle
oodle <input> <output> <decompressed_size>
```

The input is the raw stream with no size prefix. The argument order matches the
`ooz_raw` helper in `SRETSS/replay-viewer/scripts/bin`, so `OOZ_RAW_BIN` can
point at this binary with no change to the Python scripts.

## Tests

The reference corpus is 314 MB, so it stays outside this repository.
`testdata/reference.sha256` records the expected output of each file.

```
git clone --depth 1 https://github.com/powzix/ooz /tmp/ooz
OODLE_CORPUS=/tmp/ooz/testdata go test ./...
```

That runs 48 cases: 12 corpora (text and binary) times the four codecs, each
compressed by the real Oodle encoder. Between them they exercise every entropy
chunk type (Huffman with both code length encodings, TANS, RLE, recursive,
multi array), both Kraken literal modes, both Mermaid modes, and all six
Leviathan literal modes in both single command and multi command form.

The decoder was also checked against 400 full length streams taken from a War
Thunder install (`.ddsx`, `.grp`, `.dxp.bin`, `levels/*.bin`). Every one decodes
byte-for-byte the same as the C original. Those assets are compressed almost
entirely with Leviathan; Kraken appears rarely and Mermaid and Selkie not at
all.

## Files

| File | Contents |
| --- | --- |
| `oodle.go` | public API, block and quantum headers, the decode loop |
| `bits.go` | bit readers and safe unaligned reads |
| `huff.go` | Huffman tables and the three stream Huffman decoder |
| `tans.go` | TANS table build and decode |
| `entropy.go` | entropy chunk dispatch, multi array, RLE, recursive |
| `kraken.go` | Kraken LZ table and match copying |
| `mermaid.go` | Mermaid and Selkie |
| `leviathan.go` | Leviathan and its six literal modes |
| `lzcopy.go` | the overlong copy primitives the format depends on |

## A note on memory

The format is built around loads and stores that run past the end of a buffer.
Every buffer here carries 128 bytes of slack inside its own length, so those
reads and writes stay in the same array. Streams are held as a whole buffer plus
an index, never as a sub-slice, so the slack stays reachable. Reads before the
start of a buffer return zero, and the bits that come from there are always
shifted away again.
