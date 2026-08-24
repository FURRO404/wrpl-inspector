// Package oodle decodes Oodle LZ streams: Kraken, Mermaid, Selkie and
// Leviathan. It is a Go port of the reverse engineered decoder in
// github.com/powzix/ooz. LZNA and Bitknit are not supported.
package oodle

import (
	"errors"
	"fmt"
)

// resultShortStream is the quantum decoder result for a stream that ends before
// the requested output size.
const resultShortStream = -2

const (
	scratchSize = 0x6C000
	// safeSpace is the number of bytes the decoder may write past the end of
	// the output. The C original names the same constant SAFE_SPACE.
	safeSpace = 128
)

// Decoder type ids as they appear in the block header.
const (
	decoderLZNA      = 5
	decoderKraken    = 6
	decoderMermaid   = 10
	decoderBitknit   = 11
	decoderLeviathan = 12
)

var (
	// ErrCorrupt reports a stream the decoder could not follow.
	ErrCorrupt = errors.New("oodle: corrupt or unsupported stream")
	// ErrUnsupported reports a codec this package does not implement.
	ErrUnsupported = errors.New("oodle: unsupported codec")
	// ErrShortStream reports that the stream holds fewer bytes than the caller
	// asked for. Oodle streams do not record their decoded size, so this
	// usually means the size came from the wrong place.
	ErrShortStream = errors.New("oodle: stream holds fewer bytes than requested")
)

type blockHeader struct {
	decoderType    int
	restartDecoder bool
	uncompressed   bool
	useChecksums   bool
}

type quantumHeader struct {
	compressedSize     uint32
	checksum           uint32
	flag1              uint8
	flag2              uint8
	wholeMatchDistance uint32
}

func parseBlockHeader(hdr *blockHeader, s []byte, p int) int {
	b := int(at(s, p))
	if b&0xF != 0xC {
		return -1
	}
	if (b>>4)&3 != 0 {
		return -1
	}
	hdr.restartDecoder = (b>>7)&1 != 0
	hdr.uncompressed = (b>>6)&1 != 0
	b = int(at(s, p+1))
	hdr.decoderType = b & 0x7F
	hdr.useChecksums = b>>7 != 0
	switch hdr.decoderType {
	case decoderKraken, decoderMermaid, decoderLZNA, decoderBitknit, decoderLeviathan:
	default:
		return -1
	}
	return p + 2
}

func parseQuantumHeader(hdr *quantumHeader, s []byte, p int, useChecksum bool) int {
	v := rd24BE(s, p)
	size := v & 0x3FFFF
	if size != 0x3ffff {
		hdr.compressedSize = size + 1
		hdr.flag1 = uint8((v >> 18) & 1)
		hdr.flag2 = uint8((v >> 19) & 1)
		if useChecksum {
			hdr.checksum = rd24BE(s, p+3)
			return p + 6
		}
		return p + 3
	}
	v >>= 18
	if v == 1 {
		// memset quantum
		hdr.checksum = uint32(at(s, p+3))
		hdr.compressedSize = 0
		hdr.wholeMatchDistance = 0
		return p + 4
	}
	return -1
}

func lznaParseWholeMatchInfo(s []byte, p int) (dist uint32, n int) {
	v := uint32(at(s, p))<<8 | uint32(at(s, p+1))

	if v < 0x8000 {
		x := uint32(0)
		pos := uint(0)
		var b uint32
		for {
			b = uint32(at(s, p+2))
			p++
			if b&0x80 != 0 {
				break
			}
			x += (b + 0x80) << pos
			pos += 7
		}
		x += (b - 128) << pos
		return 0x8000 + v + (x << 15) + 1, p + 2
	}
	return v - 0x8000 + 1, p + 2
}

func lznaParseQuantumHeader(hdr *quantumHeader, s []byte, p int, useChecksum bool, rawLen int) int {
	v := uint32(at(s, p))<<8 | uint32(at(s, p+1))
	size := v & 0x3FFF
	if size != 0x3fff {
		hdr.compressedSize = size + 1
		hdr.flag1 = uint8((v >> 14) & 1)
		hdr.flag2 = uint8((v >> 15) & 1)
		if useChecksum {
			hdr.checksum = rd24BE(s, p+2)
			return p + 5
		}
		return p + 2
	}
	v >>= 14
	switch v {
	case 0:
		dist, n := lznaParseWholeMatchInfo(s, p+2)
		hdr.wholeMatchDistance = dist
		hdr.compressedSize = 0
		return n
	case 1:
		hdr.checksum = uint32(at(s, p+2))
		hdr.compressedSize = 0
		hdr.wholeMatchDistance = 0
		return p + 3
	case 2:
		hdr.compressedSize = uint32(rawLen)
		return p + 2
	}
	return -1
}

func copyWholeMatch(o []byte, dst int, offset uint32, length int) {
	oc := o
	src := dst - int(offset)
	i := 0
	if offset >= 8 {
		for ; i+8 <= length; i += 8 {
			wr64(o, dst+i, rd64(o, src+i))
		}
	}
	for ; i < length; i++ {
		oc[dst+i] = oc[src+i]
	}
}

type decoder struct {
	srcUsed int
	dstUsed int
	scr     scratch
	hdr     blockHeader
	// unsupported holds the decoder type when the stream asks for a codec this
	// package leaves out.
	unsupported int
	// short records that a decoder read past the end of a literal stream.
	short bool
}

func newDecoder() *decoder {
	d := &decoder{}
	d.scr = scratch{buf: make([]byte, scratchSize+safeSpace), cur: 0, end: scratchSize, pool: &bufPool{}}
	d.scr.buf = d.scr.buf[: scratchSize : scratchSize+safeSpace]
	return d
}

// decodeStep decodes one quantum. It reports false on a broken stream, and sets
// srcUsed to zero when the input holds too few bytes to make progress.
func (d *decoder) decodeStep(o []byte, offset int, dstBytesLeftIn int, s []byte, src, srcEnd int) bool {
	srcIn := src
	var qhdr quantumHeader

	if offset&0x3FFFF == 0 {
		src = parseBlockHeader(&d.hdr, s, src)
		if src < 0 {
			return false
		}
	}

	isKrakenDecoder := d.hdr.decoderType == decoderKraken ||
		d.hdr.decoderType == decoderMermaid ||
		d.hdr.decoderType == decoderLeviathan

	quantumSize := 0x4000
	if isKrakenDecoder {
		quantumSize = 0x40000
	}
	dstBytesLeft := minInt(quantumSize, dstBytesLeftIn)

	if d.hdr.uncompressed {
		if srcEnd-src < dstBytesLeft {
			d.srcUsed, d.dstUsed = 0, 0
			return true
		}
		copy(o[offset:offset+dstBytesLeft], s[src:src+dstBytesLeft])
		d.srcUsed = (src - srcIn) + dstBytesLeft
		d.dstUsed = dstBytesLeft
		return true
	}

	if isKrakenDecoder {
		src = parseQuantumHeader(&qhdr, s, src, d.hdr.useChecksums)
	} else {
		src = lznaParseQuantumHeader(&qhdr, s, src, d.hdr.useChecksums, dstBytesLeft)
	}

	if src < 0 || src > srcEnd {
		return false
	}

	// Too few bytes to make progress?
	if uint32(srcEnd-src) < qhdr.compressedSize {
		d.srcUsed, d.dstUsed = 0, 0
		return true
	}

	if qhdr.compressedSize > uint32(dstBytesLeft) {
		return false
	}

	if qhdr.compressedSize == 0 {
		if qhdr.wholeMatchDistance != 0 {
			if qhdr.wholeMatchDistance > uint32(offset) {
				return false
			}
			copyWholeMatch(o, offset, qhdr.wholeMatchDistance, dstBytesLeft)
		} else {
			fillByte(o, offset, byte(qhdr.checksum), dstBytesLeft)
		}
		d.srcUsed = src - srcIn
		d.dstUsed = dstBytesLeft
		return true
	}

	if qhdr.compressedSize == uint32(dstBytesLeft) {
		copy(o[offset:offset+dstBytesLeft], s[src:src+dstBytesLeft])
		d.srcUsed = (src - srcIn) + dstBytesLeft
		d.dstUsed = dstBytesLeft
		return true
	}

	var n int
	srcQEnd := src + int(qhdr.compressedSize)
	switch d.hdr.decoderType {
	case decoderKraken:
		n = krakenDecodeQuantum(o, offset, offset+dstBytesLeft, 0, s, src, srcQEnd, d.scr)
	case decoderMermaid:
		n = mermaidDecodeQuantum(o, offset, offset+dstBytesLeft, 0, s, src, srcQEnd, d.scr)
	case decoderLeviathan:
		n = leviathanDecodeQuantum(o, offset, offset+dstBytesLeft, 0, s, src, srcQEnd, d.scr)
	default:
		d.unsupported = d.hdr.decoderType
		return false
	}

	if n == resultShortStream {
		d.short = true
		return false
	}
	if n != int(qhdr.compressedSize) {
		return false
	}

	d.srcUsed = (src - srcIn) + n
	d.dstUsed = dstBytesLeft
	return true
}

func padded(src []byte) []byte {
	b := make([]byte, len(src)+safeSpace)
	copy(b, src)
	return b[: len(src) : len(src)+safeSpace]
}

// Decompress decodes an Oodle LZ stream into exactly dstSize bytes. Input bytes
// after the last quantum the output needs are ignored, which the DDSx texture
// streams in War Thunder rely on. Use DecompressStrict to reject them.
func Decompress(src []byte, dstSize int) ([]byte, error) {
	out, _, err := decompress(src, dstSize)
	return out, err
}

// DecompressStrict is Decompress, but it also reports an error when the input
// holds bytes after the stream.
func DecompressStrict(src []byte, dstSize int) ([]byte, error) {
	out, used, err := decompress(src, dstSize)
	if err != nil {
		return nil, err
	}
	if used != len(src) {
		return nil, fmt.Errorf("%w: %d input bytes left after the stream", ErrCorrupt, len(src)-used)
	}
	return out, nil
}

func decompress(src []byte, dstSize int) (out []byte, srcUsed int, err error) {
	if dstSize < 0 {
		return nil, 0, fmt.Errorf("%w: negative size", ErrCorrupt)
	}
	if dstSize == 0 {
		return []byte{}, 0, nil
	}

	s := padded(src)
	// The output buffer also carries the slack, so the copy loops may run past
	// the last byte without leaving the array.
	o := make([]byte, dstSize+safeSpace)

	if err := checkCodec(s); err != nil {
		return nil, 0, err
	}

	d := newDecoder()
	offset := 0
	p := 0
	srcLen := len(src)
	want := dstSize

	for dstSize != 0 {
		if !d.decodeStep(o, offset, dstSize, s, p, srcLen) {
			if name := codecName(d.unsupported); name != "" {
				return nil, 0, fmt.Errorf("%w: %s", ErrUnsupported, name)
			}
			if d.short {
				return nil, 0, fmt.Errorf("%w: it ends before output offset %d",
					ErrShortStream, offset+dstSize)
			}
			return nil, 0, fmt.Errorf("%w at output offset %d", ErrCorrupt, offset)
		}
		if d.srcUsed == 0 {
			return nil, 0, fmt.Errorf("%w: input ran out at output offset %d", ErrCorrupt, offset)
		}
		p += d.srcUsed
		dstSize -= d.dstUsed
		offset += d.dstUsed
	}
	return o[:want], p, nil
}

// codecName names the two codecs this package leaves out.
func codecName(decoderType int) string {
	switch decoderType {
	case decoderLZNA:
		return "LZNA"
	case decoderBitknit:
		return "Bitknit"
	}
	return ""
}

// checkCodec rejects an unsupported codec before any work starts.
func checkCodec(s []byte) error {
	var hdr blockHeader
	if parseBlockHeader(&hdr, s, 0) < 0 {
		return fmt.Errorf("%w: bad block header", ErrCorrupt)
	}
	if name := codecName(hdr.decoderType); name != "" {
		return fmt.Errorf("%w: %s", ErrUnsupported, name)
	}
	return nil
}
