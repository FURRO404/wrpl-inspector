package oodle

import "math/bits"

// scratch is the working area the entropy decoders carve buffers out of. It is
// passed by value, so a callee that advances cur does not affect its caller.
// The pool it points at is shared, and holds the typed arrays that would
// otherwise be allocated once per chunk.
type scratch struct {
	buf      []byte
	cur, end int
	pool     *bufPool
}

func (s scratch) avail() int { return s.end - s.cur }

// window returns the same scratch area narrowed to [cur, end).
func (s scratch) window(cur, end int) scratch {
	s.cur, s.end = cur, end
	return s
}

// bufPool holds the working arrays the LZ and entropy stages need. Each one is
// live in at most one stage at a time, so a single copy of each is enough.
type bufPool struct {
	offs      []int32
	lens      []int32
	intervals []uint32
	far1      []uint32
	far2      []uint32
	tansLut   []tansLutEnt
}

func growI32(b []int32, n int) []int32 {
	if cap(b) < n {
		return make([]int32, n, n+n/2+64)
	}
	return b[:n]
}

func growU32(b []uint32, n int) []uint32 {
	if cap(b) < n {
		return make([]uint32, n, n+n/2+64)
	}
	return b[:n]
}

// stream is a position inside one of the byte arrays in play: the source, the
// scratch area or the output window.
type stream struct {
	buf []byte
	i   int
}

func (s stream) byteAt(k int) byte { return at(s.buf, s.i+k) }

func sameArray(a, b []byte) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	return &a[0] == &b[0]
}

func alignUp(v, a int) int { return (v + a - 1) &^ (a - 1) }

// krakenGetBlockSize reports the decompressed size of one entropy chunk.
func krakenGetBlockSize(s []byte, src, srcEnd int, destCapacity int) (destSize, n int) {
	srcOrg := src
	var srcSize, dstSize int

	if srcEnd-src < 2 {
		return 0, -1
	}

	chunkType := (int(at(s, src)) >> 4) & 0x7
	if chunkType == 0 {
		if at(s, src) >= 0x80 {
			srcSize = ((int(at(s, src)) << 8) | int(at(s, src+1))) & 0xFFF
			src += 2
		} else {
			if srcEnd-src < 3 {
				return 0, -1
			}
			srcSize = int(rd24BE(s, src))
			if srcSize&^0x3ffff != 0 {
				return 0, -1
			}
			src += 3
		}
		if srcSize > destCapacity || srcEnd-src < srcSize {
			return 0, -1
		}
		return srcSize, src + srcSize - srcOrg
	}

	if chunkType >= 6 {
		return 0, -1
	}

	if at(s, src) >= 0x80 {
		if srcEnd-src < 3 {
			return 0, -1
		}
		b := rd24BE(s, src)
		srcSize = int(b & 0x3ff)
		dstSize = srcSize + int((b>>10)&0x3ff) + 1
		src += 3
	} else {
		if srcEnd-src < 5 {
			return 0, -1
		}
		b := uint32(at(s, src+1))<<24 | uint32(at(s, src+2))<<16 | uint32(at(s, src+3))<<8 | uint32(at(s, src+4))
		srcSize = int(b & 0x3ffff)
		dstSize = int(((b>>18)|(uint32(at(s, src))<<14))&0x3FFFF) + 1
		if srcSize >= dstSize {
			return 0, -1
		}
		src += 5
	}
	if srcEnd-src < srcSize || dstSize > destCapacity {
		return 0, -1
	}
	return dstSize, srcSize
}

func rd24BE(s []byte, i int) uint32 {
	return uint32(at(s, i))<<16 | uint32(at(s, i+1))<<8 | uint32(at(s, i+2))
}

// krakenDecodeBytes decodes one entropy chunk. out/o names the wanted
// destination; the returned stream is either that destination or, when the
// chunk is stored raw and forceMemmove is false, a window into the source.
func krakenDecodeBytes(s []byte, src, srcEnd int, out []byte, o int, outputSize int,
	forceMemmove bool, scr scratch) (n int, res stream, decodedSize int) {
	srcOrg := src
	var srcSize, dstSize int

	if srcEnd-src < 2 {
		return -1, stream{}, 0
	}

	chunkType := (int(at(s, src)) >> 4) & 0x7
	if chunkType == 0 {
		if at(s, src) >= 0x80 {
			srcSize = ((int(at(s, src)) << 8) | int(at(s, src+1))) & 0xFFF
			src += 2
		} else {
			if srcEnd-src < 3 {
				return -1, stream{}, 0
			}
			srcSize = int(rd24BE(s, src))
			if srcSize&^0x3ffff != 0 {
				return -1, stream{}, 0
			}
			src += 3
		}
		if srcSize > outputSize || srcEnd-src < srcSize {
			return -1, stream{}, 0
		}
		if forceMemmove {
			copy(out[o:o+srcSize], s[src:src+srcSize])
			res = stream{out, o}
		} else {
			res = stream{s, src}
		}
		return src + srcSize - srcOrg, res, srcSize
	}

	if at(s, src) >= 0x80 {
		if srcEnd-src < 3 {
			return -1, stream{}, 0
		}
		b := rd24BE(s, src)
		srcSize = int(b & 0x3ff)
		dstSize = srcSize + int((b>>10)&0x3ff) + 1
		src += 3
	} else {
		if srcEnd-src < 5 {
			return -1, stream{}, 0
		}
		b := uint32(at(s, src+1))<<24 | uint32(at(s, src+2))<<16 | uint32(at(s, src+3))<<8 | uint32(at(s, src+4))
		srcSize = int(b & 0x3ffff)
		dstSize = int(((b>>18)|(uint32(at(s, src))<<14))&0x3FFFF) + 1
		if srcSize >= dstSize {
			return -1, stream{}, 0
		}
		src += 5
	}
	if srcEnd-src < srcSize || dstSize > outputSize {
		return -1, stream{}, 0
	}

	if sameArray(out, scr.buf) && o == scr.cur {
		if scr.avail() < dstSize {
			return -1, stream{}, 0
		}
		scr.cur += dstSize
	}

	srcUsed := -1
	switch chunkType {
	case 2, 4:
		srcUsed = decodeBytesType12(s, src, srcSize, out, o, dstSize, chunkType>>1)
	case 5:
		srcUsed = krakDecodeRecursive(s, src, srcSize, out, o, dstSize, scr)
	case 3:
		srcUsed = krakDecodeRLE(s, src, srcSize, out, o, dstSize, scr)
	case 1:
		srcUsed = krakDecodeTans(s, src, srcSize, out, o, dstSize, scr)
	}
	if srcUsed != srcSize {
		return -1, stream{}, 0
	}
	return src + srcSize - srcOrg, stream{out, o}, dstSize
}

var bitmasks = [32]uint32{
	0x1, 0x3, 0x7, 0xf, 0x1f, 0x3f, 0x7f, 0xff,
	0x1ff, 0x3ff, 0x7ff, 0xfff, 0x1fff, 0x3fff, 0x7fff, 0xffff,
	0x1ffff, 0x3ffff, 0x7ffff, 0xfffff, 0x1fffff, 0x3fffff, 0x7fffff,
	0xffffff, 0x1ffffff, 0x3ffffff, 0x7ffffff, 0xfffffff, 0x1fffffff, 0x3fffffff, 0x7fffffff, 0xffffffff,
}

// krakenDecodeMultiArray splits one entropy stream into arrayCount arrays.
func krakenDecodeMultiArray(s []byte, src, srcEnd int, out []byte, dst, dstEnd int,
	arrayData []stream, arrayLens []int, arrayCount int,
	forceMemmove bool, scr scratch) (n, totalSizeOut int) {
	srcOrg := src

	if srcEnd-src < 4 {
		return -1, 0
	}

	numArraysInFile := int(at(s, src))
	src++
	if numArraysInFile&0x80 == 0 {
		return -1, 0
	}
	numArraysInFile &= 0x3f

	if sameArray(out, scr.buf) && dst == scr.cur {
		scr.cur += (scr.end - scr.cur - 0xc000) >> 1
		dstEnd = scr.cur
	}

	totalSize := 0

	if numArraysInFile == 0 {
		for i := 0; i < arrayCount; i++ {
			dec, chunk, decodedSize := krakenDecodeBytes(s, src, srcEnd, out, dst, dstEnd-dst, forceMemmove, scr)
			if dec < 0 {
				return -1, 0
			}
			dst += decodedSize
			arrayLens[i] = decodedSize
			arrayData[i] = chunk
			src += dec
			totalSize += decodedSize
		}
		return src - srcOrg, totalSize
	}

	// The array count is a 6 bit field, so it reaches 63. The C original sizes
	// these at 32 and writes past them.
	var entropyArrayData [64]stream
	var entropyArraySize [64]uint32

	scratchCur := scr.cur

	for i := 0; i < numArraysInFile; i++ {
		inner := scr.window(scratchCur, scr.end)
		dec, chunk, decodedSize := krakenDecodeBytes(s, src, srcEnd, scr.buf, scratchCur, scr.end-scratchCur, forceMemmove, inner)
		if dec < 0 {
			return -1, 0
		}
		entropyArrayData[i] = chunk
		entropyArraySize[i] = uint32(decodedSize)
		scratchCur += decodedSize
		totalSize += decodedSize
		src += dec
	}
	totalSizeOut = totalSize

	if srcEnd-src < 3 {
		return -1, 0
	}

	q := int(rd16(s, src))
	src += 2

	outSize, ok := krakenGetBlockSize(s, src, srcEnd, totalSize)
	if ok < 0 {
		return -1, 0
	}
	numIndexes := outSize

	numLens := numIndexes - arrayCount
	if numLens < 1 {
		return -1, 0
	}

	if scr.end-scratchCur < numIndexes {
		return -1, 0
	}
	intervalLenlog2 := stream{scr.buf, scratchCur}
	scratchCur += numIndexes

	if scr.end-scratchCur < numIndexes {
		return -1, 0
	}
	intervalIndexes := stream{scr.buf, scratchCur}
	scratchCur += numIndexes

	if q&0x8000 != 0 {
		inner := scr.window(scratchCur, scr.end)
		nn, chunk, sizeOut := krakenDecodeBytes(s, src, srcEnd, intervalIndexes.buf, intervalIndexes.i, numIndexes, false, inner)
		if nn < 0 || sizeOut != numIndexes {
			return -1, 0
		}
		intervalIndexes = chunk
		src += nn

		ic := intervalIndexes.buf
		lc := intervalLenlog2.buf
		for i := 0; i < numIndexes; i++ {
			t := int(ic[intervalIndexes.i+i])
			lc[intervalLenlog2.i+i] = byte(t >> 4)
			ic[intervalIndexes.i+i] = byte(t & 0xF)
		}

		numLens = numIndexes
	} else {
		lenlog2Chunksize := numIndexes - arrayCount

		inner := scr.window(scratchCur, scr.end)
		nn, chunk, sizeOut := krakenDecodeBytes(s, src, srcEnd, intervalIndexes.buf, intervalIndexes.i, numIndexes, false, inner)
		if nn < 0 || sizeOut != numIndexes {
			return -1, 0
		}
		intervalIndexes = chunk
		src += nn

		nn, chunk, sizeOut = krakenDecodeBytes(s, src, srcEnd, intervalLenlog2.buf, intervalLenlog2.i, lenlog2Chunksize, false, inner)
		if nn < 0 || sizeOut != lenlog2Chunksize {
			return -1, 0
		}
		intervalLenlog2 = chunk
		src += nn

		for i := 0; i < lenlog2Chunksize; i++ {
			if intervalLenlog2.byteAt(i) > 16 {
				return -1, 0
			}
		}
	}

	if scr.end-scratchCur < 4 {
		return -1, 0
	}
	scratchCur = alignUp(scratchCur, 4)
	if scr.end-scratchCur < numLens*4 {
		return -1, 0
	}
	scr.pool.intervals = growU32(scr.pool.intervals, numLens)
	decodedIntervals := scr.pool.intervals

	varbitsComplen := q & 0x3FFF
	if srcEnd-src < varbitsComplen {
		return -1, 0
	}

	f := src
	bitsF := uint32(0)
	bitposF := 24

	srcEndActual := src + varbitsComplen

	b := srcEndActual
	bitsB := uint32(0)
	bitposB := 24

	i := 0
	for ; i+2 <= numLens; i += 2 {
		bitsF |= rdbe32(s, f) >> uint(24-bitposF)
		f += (bitposF + 7) >> 3

		bitsB |= rd32(s, b-4) >> uint(24-bitposB)
		b -= (bitposB + 7) >> 3

		numbitsF := int(intervalLenlog2.byteAt(i + 0))
		numbitsB := int(intervalLenlog2.byteAt(i + 1))

		bitsF = bits.RotateLeft32(bitsF|1, numbitsF)
		bitposF += numbitsF - 8*((bitposF+7)>>3)

		bitsB = bits.RotateLeft32(bitsB|1, numbitsB)
		bitposB += numbitsB - 8*((bitposB+7)>>3)

		valueF := bitsF & bitmasks[numbitsF]
		bitsF &^= bitmasks[numbitsF]

		valueB := bitsB & bitmasks[numbitsB]
		bitsB &^= bitmasks[numbitsB]

		decodedIntervals[i+0] = valueF
		decodedIntervals[i+1] = valueB
	}

	if i < numLens {
		bitsF |= rdbe32(s, f) >> uint(24-bitposF)
		numbitsF := int(intervalLenlog2.byteAt(i))
		bitsF = bits.RotateLeft32(bitsF|1, numbitsF)
		decodedIntervals[i+0] = bitsF & bitmasks[numbitsF]
	}

	if intervalIndexes.byteAt(numIndexes-1) != 0 {
		return -1, 0
	}

	indi, leni := 0, 0
	incrementLeni := 0
	if q&0x8000 != 0 {
		incrementLeni = 1
	}

	for arri := 0; arri < arrayCount; arri++ {
		arrayData[arri] = stream{out, dst}
		if indi >= numIndexes {
			return -1, 0
		}

		for {
			source := int(intervalIndexes.byteAt(indi))
			indi++
			if source == 0 {
				break
			}
			if source > numArraysInFile {
				return -1, 0
			}
			if leni >= numLens {
				return -1, 0
			}
			curLen := int(decodedIntervals[leni])
			leni++
			bytesLeft := int(entropyArraySize[source-1])
			if curLen > bytesLeft || curLen > dstEnd-dst {
				return -1, 0
			}
			blksrc := entropyArrayData[source-1]
			entropyArraySize[source-1] -= uint32(curLen)
			entropyArrayData[source-1].i += curLen
			copy(out[dst:dst+curLen], blksrc.buf[blksrc.i:blksrc.i+curLen])
			dst += curLen
		}
		leni += incrementLeni
		arrayLens[arri] = dst - arrayData[arri].i
	}

	if indi != numIndexes || leni != numLens {
		return -1, 0
	}

	for i := 0; i < numArraysInFile; i++ {
		if entropyArraySize[i] != 0 {
			return -1, 0
		}
	}
	return srcEndActual - srcOrg, totalSizeOut
}

func krakDecodeRecursive(s []byte, src, srcSize int, out []byte, o, outputSize int, scr scratch) int {
	srcOrg := src
	outputEnd := o + outputSize
	srcEnd := src + srcSize

	if srcSize < 6 {
		return -1
	}

	n := int(at(s, src)) & 0x7f
	if n < 2 {
		return -1
	}

	if at(s, src)&0x80 == 0 {
		src++
		for ; n > 0; n-- {
			dec, _, decodedSize := krakenDecodeBytes(s, src, srcEnd, out, o, outputEnd-o, true, scr)
			if dec < 0 {
				return -1
			}
			o += decodedSize
			src += dec
		}
		if o != outputEnd {
			return -1
		}
		return src - srcOrg
	}

	var arrayData [1]stream
	var arrayLen [1]int
	dec, decodedSize := krakenDecodeMultiArray(s, src, srcEnd, out, o, outputEnd, arrayData[:], arrayLen[:], 1, true, scr)
	if dec < 0 {
		return -1
	}
	o += decodedSize
	if o != outputEnd {
		return -1
	}
	return dec
}

func krakDecodeRLE(s []byte, src, srcSize int, out []byte, dst, dstSize int, scr scratch) int {
	oc := out
	if srcSize <= 1 {
		if srcSize != 1 {
			return -1
		}
		fillByte(out, dst, at(s, src), dstSize)
		return 1
	}
	dstEnd := dst + dstSize
	cmd := stream{s, src + 1}
	cmdEnd := stream{s, src + srcSize}

	if at(s, src) != 0 {
		nn, chunk, decSize := krakenDecodeBytes(s, src, src+srcSize, scr.buf, scr.cur, scr.avail(), true, scr)
		if nn <= 0 {
			return -1
		}
		cmdLen := srcSize - nn + decSize
		if cmdLen > scr.avail() {
			return -1
		}
		copy(chunk.buf[chunk.i+decSize:chunk.i+cmdLen],
			s[src+nn:src+srcSize])
		cmd = chunk
		cmdEnd = stream{chunk.buf, chunk.i + cmdLen}
	}

	cb := cmd.buf
	ci, ce := cmd.i, cmdEnd.i

	rleByte := byte(0)

	for ci < ce {
		c := uint32(cb[ce-1])
		if c-1 >= 0x2f {
			ce--
			bytesToCopy := int((^c) & 0xF)
			bytesToRle := int(c >> 4)
			if dstEnd-dst < bytesToCopy+bytesToRle || ce-ci < bytesToCopy {
				return -1
			}
			copy(oc[dst:dst+bytesToCopy], cb[ci:ci+bytesToCopy])
			ci += bytesToCopy
			dst += bytesToCopy
			for k := 0; k < bytesToRle; k++ {
				oc[dst+k] = rleByte
			}
			dst += bytesToRle
		} else if c >= 0x10 {
			data := uint32(rd16(cmd.buf, ce-2)) - 4096
			ce -= 2
			bytesToCopy := int(data & 0x3F)
			bytesToRle := int(data >> 6)
			if dstEnd-dst < bytesToCopy+bytesToRle || ce-ci < bytesToCopy {
				return -1
			}
			copy(oc[dst:dst+bytesToCopy], cb[ci:ci+bytesToCopy])
			ci += bytesToCopy
			dst += bytesToCopy
			for k := 0; k < bytesToRle; k++ {
				oc[dst+k] = rleByte
			}
			dst += bytesToRle
		} else if c == 1 {
			rleByte = cb[ci]
			ci++
			ce--
		} else if c >= 9 {
			bytesToRle := int(uint32(rd16(cmd.buf, ce-2))-0x8ff) * 128
			ce -= 2
			if dstEnd-dst < bytesToRle {
				return -1
			}
			for k := 0; k < bytesToRle; k++ {
				oc[dst+k] = rleByte
			}
			dst += bytesToRle
		} else {
			bytesToCopy := int(uint32(rd16(cmd.buf, ce-2))-511) * 64
			ce -= 2
			if ce-ci < bytesToCopy || dstEnd-dst < bytesToCopy {
				return -1
			}
			copy(oc[dst:dst+bytesToCopy], cb[ci:ci+bytesToCopy])
			dst += bytesToCopy
			ci += bytesToCopy
		}
	}
	if ce != ci {
		return -1
	}
	if dst != dstEnd {
		return -1
	}
	return srcSize
}
