package oodle

import (
	"encoding/binary"
	"math/bits"
)

// The C original does unaligned loads that can run a few bytes past the end of
// a stream, and in two places a few bytes before the start. Every buffer here
// carries safeSpace bytes of slack inside its own length, so a read past the
// end stays in the same array and returns the zero padding. A read before the
// start returns zero; the bits that come from there are always shifted away
// again.

// at reads one byte, or 0 when the index is outside the buffer.
func at(s []byte, i int) byte {
	if uint(i) >= uint(len(s)) {
		return 0
	}
	return s[i]
}

func rd16(s []byte, i int) uint16 {
	if uint(i)+2 <= uint(len(s)) {
		return binary.LittleEndian.Uint16(s[i:])
	}
	return rd16Edge(s, i)
}

func rd16Edge(s []byte, i int) uint16 {
	return uint16(at(s, i)) | uint16(at(s, i+1))<<8
}

func rd32(s []byte, i int) uint32 {
	if uint(i)+4 <= uint(len(s)) {
		return binary.LittleEndian.Uint32(s[i:])
	}
	return rd32Edge(s, i)
}

// rd32Edge handles the reads that fall outside the buffer. Keeping them out of
// rd32 keeps rd32 small enough for the compiler to inline.
func rd32Edge(s []byte, i int) uint32 {
	return uint32(at(s, i)) | uint32(at(s, i+1))<<8 | uint32(at(s, i+2))<<16 | uint32(at(s, i+3))<<24
}

// rdbe32 is _byteswap_ulong(*(uint32*)&s[i]).
func rdbe32(s []byte, i int) uint32 {
	return bits.ReverseBytes32(rd32(s, i))
}

func rd64(s []byte, i int) uint64 {
	if uint(i)+8 <= uint(len(s)) {
		return binary.LittleEndian.Uint64(s[i:])
	}
	return rd64Edge(s, i)
}

func rd64Edge(s []byte, i int) uint64 {
	return uint64(rd32(s, i)) | uint64(rd32(s, i+4))<<32
}

func wr32(s []byte, i int, v uint32) {
	binary.LittleEndian.PutUint32(s[i:], v)
}

func wr64(s []byte, i int, v uint64) {
	binary.LittleEndian.PutUint64(s[i:], v)
}

// fillByte writes n copies of v at off.
func fillByte(dst []byte, off int, v byte, n int) {
	if n <= 0 {
		return
	}
	d := dst[off : off+n]
	d[0] = v
	for k := 1; k < len(d); k *= 2 {
		copy(d[k:], d[:k])
	}
}

func bsr(x uint32) int { return 31 - bits.LeadingZeros32(x) }
func bsf(x uint32) int { return bits.TrailingZeros32(x) }
func clz(x uint32) int { return bits.LeadingZeros32(x) }

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// bitReader reads bits MSB first out of s, forwards or backwards.
type bitReader struct {
	s      []byte
	p      int
	pEnd   int
	bits   uint32
	bitpos int
}

func (b *bitReader) refill() {
	for b.bitpos > 0 {
		var v uint32
		if b.p < b.pEnd {
			v = uint32(at(b.s, b.p))
		}
		b.bits |= v << uint(b.bitpos)
		b.bitpos -= 8
		b.p++
	}
}

func (b *bitReader) refillBackwards() {
	for b.bitpos > 0 {
		b.p--
		var v uint32
		if b.p >= b.pEnd {
			v = uint32(at(b.s, b.p))
		}
		b.bits |= v << uint(b.bitpos)
		b.bitpos -= 8
	}
}

func (b *bitReader) readBit() int {
	b.refill()
	return b.readBitNoRefill()
}

func (b *bitReader) readBitNoRefill() int {
	r := int(b.bits >> 31)
	b.bits <<= 1
	b.bitpos++
	return r
}

func (b *bitReader) readBitsNoRefill(n int) int {
	r := int(b.bits >> uint(32-n))
	b.bits <<= uint(n)
	b.bitpos += n
	return r
}

// readBitsNoRefillZero allows n == 0.
func (b *bitReader) readBitsNoRefillZero(n int) int {
	r := int(b.bits >> 1 >> uint(31-n))
	b.bits <<= uint(n)
	b.bitpos += n
	return r
}

func (b *bitReader) readMoreThan24Bits(n int) uint32 {
	var rv uint32
	if n <= 24 {
		rv = uint32(b.readBitsNoRefillZero(n))
	} else {
		rv = uint32(b.readBitsNoRefill(24)) << uint(n-24)
		b.refill()
		rv += uint32(b.readBitsNoRefill(n - 24))
	}
	b.refill()
	return rv
}

func (b *bitReader) readMoreThan24BitsB(n int) uint32 {
	var rv uint32
	if n <= 24 {
		rv = uint32(b.readBitsNoRefillZero(n))
	} else {
		rv = uint32(b.readBitsNoRefill(24)) << uint(n-24)
		b.refillBackwards()
		rv += uint32(b.readBitsNoRefill(n - 24))
	}
	b.refillBackwards()
	return rv
}

func (b *bitReader) readDistance(v uint32) uint32 {
	var w, m, n, rv uint32
	if v < 0xF0 {
		n = (v >> 4) + 4
		w = bits.RotateLeft32(b.bits|1, int(n))
		b.bitpos += int(n)
		m = (2 << n) - 1
		b.bits = w &^ m
		rv = ((w&m)<<4 + (v & 0xF)) - 248
	} else {
		n = v - 0xF0 + 4
		w = bits.RotateLeft32(b.bits|1, int(n))
		b.bitpos += int(n)
		m = (2 << n) - 1
		b.bits = w &^ m
		rv = 8322816 + ((w & m) << 12)
		b.refill()
		rv += b.bits >> 20
		b.bitpos += 12
		b.bits <<= 12
	}
	b.refill()
	return rv
}

func (b *bitReader) readDistanceB(v uint32) uint32 {
	var w, m, n, rv uint32
	if v < 0xF0 {
		n = (v >> 4) + 4
		w = bits.RotateLeft32(b.bits|1, int(n))
		b.bitpos += int(n)
		m = (2 << n) - 1
		b.bits = w &^ m
		rv = ((w&m)<<4 + (v & 0xF)) - 248
	} else {
		n = v - 0xF0 + 4
		w = bits.RotateLeft32(b.bits|1, int(n))
		b.bitpos += int(n)
		m = (2 << n) - 1
		b.bits = w &^ m
		rv = 8322816 + ((w & m) << 12)
		b.refillBackwards()
		rv += b.bits >> 20
		b.bitpos += 12
		b.bits <<= 12
	}
	b.refillBackwards()
	return rv
}

func (b *bitReader) readLength() (uint32, bool) {
	n := clz(b.bits)
	if n > 12 {
		return 0, false
	}
	b.bitpos += n
	b.bits <<= uint(n)
	b.refill()
	n += 7
	b.bitpos += n
	rv := (b.bits >> uint(32-n)) - 64
	b.bits <<= uint(n)
	b.refill()
	return rv, true
}

func (b *bitReader) readLengthB() (uint32, bool) {
	n := clz(b.bits)
	if n > 12 {
		return 0, false
	}
	b.bitpos += n
	b.bits <<= uint(n)
	b.refillBackwards()
	n += 7
	b.bitpos += n
	rv := (b.bits >> uint(32-n)) - 64
	b.bits <<= uint(n)
	b.refillBackwards()
	return rv, true
}

func (b *bitReader) readFluff(numSymbols int) int {
	if numSymbols == 256 {
		return 0
	}
	x := 257 - numSymbols
	if x > numSymbols {
		x = numSymbols
	}
	x *= 2
	y := bsr(uint32(x-1)) + 1
	v := b.bits >> uint(32-y)
	z := uint32(1<<uint(y)) - uint32(x)
	if (v >> 1) >= z {
		b.bits <<= uint(y)
		b.bitpos += y
		return int(v - z)
	}
	b.bits <<= uint(y - 1)
	b.bitpos += y - 1
	return int(v >> 1)
}

// bitReader2 is the byte-oriented reader the Golomb-Rice code uses.
type bitReader2 struct {
	s      []byte
	p      int
	pEnd   int
	bitpos uint32
}
