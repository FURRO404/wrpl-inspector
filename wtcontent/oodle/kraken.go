package oodle

// sizeofKrakenLzTable and the other sizeof values below mirror the C structs
// the original overlays on the scratch area. They only affect how much scratch
// space is left, so they must keep the same values.
const (
	sizeofKrakenLzTable    = 48
	sizeofLeviathanLzTable = 376
	sizeofMermaidLzTable   = 104
)

type krakenLzTable struct {
	cmdStream     stream
	cmdStreamSize int

	offsStream     []int32
	offsStreamSize int

	litStream     stream
	litStreamSize int

	lenStream     []int32
	lenStreamSize int
}

func combineScaledOffsetArrays(offs []int32, scale int, lowBits stream) {
	for i := range offs {
		offs[i] = int32(scale)*offs[i] - int32(lowBits.byteAt(i))
	}
}

// krakenUnpackOffsets expands the packed 8 bit offsets and lengths into 32 bit.
func krakenUnpackOffsets(s []byte, src, srcEnd int,
	packedOffs stream, packedOffsExtra stream, packedOffsSize int,
	multiDistScale int,
	packedLitlen stream, packedLitlenSize int,
	offsStream []int32, lenStream []int32,
	excessFlag bool) bool {

	var bitsA, bitsB bitReader
	u32LenStreamSize := 0

	bitsA.s = s
	bitsA.bitpos = 24
	bitsA.bits = 0
	bitsA.p = src
	bitsA.pEnd = srcEnd
	bitsA.refill()

	bitsB.s = s
	bitsB.bitpos = 24
	bitsB.bits = 0
	bitsB.p = srcEnd
	bitsB.pEnd = src
	bitsB.refillBackwards()

	if !excessFlag {
		if bitsB.bits < 0x2000 {
			return false
		}
		n := clz(bitsB.bits)
		bitsB.bitpos += n
		bitsB.bits <<= uint(n)
		bitsB.refillBackwards()
		n++
		u32LenStreamSize = int(bitsB.bits>>uint(32-n)) - 1
		bitsB.bitpos += n
		bitsB.bits <<= uint(n)
		bitsB.refillBackwards()
	}

	oi := 0
	if multiDistScale == 0 {
		for pi := 0; pi != packedOffsSize; {
			offsStream[oi] = -int32(bitsA.readDistance(uint32(packedOffs.byteAt(pi))))
			oi++
			pi++
			if pi == packedOffsSize {
				break
			}
			offsStream[oi] = -int32(bitsB.readDistanceB(uint32(packedOffs.byteAt(pi))))
			oi++
			pi++
		}
	} else {
		for pi := 0; pi != packedOffsSize; {
			cmd := uint32(packedOffs.byteAt(pi))
			pi++
			if (cmd >> 3) > 26 {
				return false
			}
			offs := ((8 + (cmd & 7)) << (cmd >> 3)) | bitsA.readMoreThan24Bits(int(cmd>>3))
			offsStream[oi] = 8 - int32(offs)
			oi++
			if pi == packedOffsSize {
				break
			}
			cmd = uint32(packedOffs.byteAt(pi))
			pi++
			if (cmd >> 3) > 26 {
				return false
			}
			offs = ((8 + (cmd & 7)) << (cmd >> 3)) | bitsB.readMoreThan24BitsB(int(cmd>>3))
			offsStream[oi] = 8 - int32(offs)
			oi++
		}
		if multiDistScale != 1 {
			combineScaledOffsetArrays(offsStream[:oi], multiDistScale, packedOffsExtra)
		}
	}

	if u32LenStreamSize > 512 {
		return false
	}
	var u32LenStream [512]uint32
	i := 0
	for ; i+1 < u32LenStreamSize; i += 2 {
		v, ok := bitsA.readLength()
		if !ok {
			return false
		}
		u32LenStream[i+0] = v
		v, ok = bitsB.readLengthB()
		if !ok {
			return false
		}
		u32LenStream[i+1] = v
	}
	if i < u32LenStreamSize {
		v, ok := bitsA.readLength()
		if !ok {
			return false
		}
		u32LenStream[i+0] = v
	}

	bitsA.p -= (24 - bitsA.bitpos) >> 3
	bitsB.p += (24 - bitsB.bitpos) >> 3

	if bitsA.p != bitsB.p {
		return false
	}

	li := 0
	for i := 0; i < packedLitlenSize; i++ {
		v := uint32(packedLitlen.byteAt(i))
		if v == 255 {
			v = u32LenStream[li] + 255
			li++
		}
		lenStream[i] = int32(v + 3)
	}
	return li == u32LenStreamSize
}

func krakenReadLzTable(mode int, s []byte, src, srcEnd int,
	out []byte, dst, dstSize, offset int,
	scr scratch, lzt *krakenLzTable) bool {

	if mode > 1 {
		return false
	}
	if srcEnd-src < 13 {
		return false
	}

	if offset == 0 {
		copy64(out, dst, s, src)
		dst += 8
		src += 8
	}

	if at(s, src)&0x80 != 0 {
		flag := at(s, src)
		src++
		if flag&0xc0 != 0x80 {
			return false // reserved flag set
		}
		return false // excess bytes not supported
	}

	// Decode the literal stream, bounded by dstSize.
	n, chunk, decodeCount := krakenDecodeBytes(s, src, srcEnd, scr.buf, scr.cur,
		minInt(scr.avail(), dstSize), true, scr)
	if n < 0 {
		return false
	}
	src += n
	lzt.litStream = chunk
	lzt.litStreamSize = decodeCount
	scr.cur += decodeCount

	// Decode the command stream, bounded by dstSize.
	n, chunk, decodeCount = krakenDecodeBytes(s, src, srcEnd, scr.buf, scr.cur,
		minInt(scr.avail(), dstSize), true, scr)
	if n < 0 {
		return false
	}
	src += n
	lzt.cmdStream = chunk
	lzt.cmdStreamSize = decodeCount
	scr.cur += decodeCount

	if srcEnd-src < 3 {
		return false
	}

	offsScaling := 0
	var packedOffsExtra stream
	var packedOffs stream

	if at(s, src)&0x80 != 0 {
		// Distances coded with two tables.
		offsScaling = int(at(s, src)) - 127
		src++

		n, packedOffs, lzt.offsStreamSize = krakenDecodeBytes(s, src, srcEnd, scr.buf, scr.cur,
			minInt(scr.avail(), lzt.cmdStreamSize), false, scr)
		if n < 0 {
			return false
		}
		src += n
		scr.cur += lzt.offsStreamSize

		if offsScaling != 1 {
			n, packedOffsExtra, decodeCount = krakenDecodeBytes(s, src, srcEnd, scr.buf, scr.cur,
				minInt(scr.avail(), lzt.offsStreamSize), false, scr)
			if n < 0 || decodeCount != lzt.offsStreamSize {
				return false
			}
			src += n
			scr.cur += decodeCount
		}
	} else {
		n, packedOffs, lzt.offsStreamSize = krakenDecodeBytes(s, src, srcEnd, scr.buf, scr.cur,
			minInt(scr.avail(), lzt.cmdStreamSize), false, scr)
		if n < 0 {
			return false
		}
		src += n
		scr.cur += lzt.offsStreamSize
	}

	// Decode the packed literal length stream, bounded by a quarter of dstSize.
	var packedLen stream
	n, packedLen, lzt.lenStreamSize = krakenDecodeBytes(s, src, srcEnd, scr.buf, scr.cur,
		minInt(scr.avail(), dstSize>>2), false, scr)
	if n < 0 {
		return false
	}
	src += n
	scr.cur += lzt.lenStreamSize

	scr.cur = alignUp(scr.cur, 16)
	scr.cur += lzt.offsStreamSize * 4
	scr.cur = alignUp(scr.cur, 16)
	scr.cur += lzt.lenStreamSize * 4

	if scr.cur+64 > scr.end {
		return false
	}

	scr.pool.offs = growI32(scr.pool.offs, lzt.offsStreamSize)
	scr.pool.lens = growI32(scr.pool.lens, lzt.lenStreamSize)
	lzt.offsStream = scr.pool.offs
	lzt.lenStream = scr.pool.lens

	return krakenUnpackOffsets(s, src, srcEnd, packedOffs, packedOffsExtra,
		lzt.offsStreamSize, offsScaling,
		packedLen, lzt.lenStreamSize,
		lzt.offsStream, lzt.lenStream, false)
}

// krakenProcessLzRunsType0 is the sub-literal variant.
func krakenProcessLzRunsType0(lzt *krakenLzTable, o []byte, dst, dstEnd, dstStart int) bool {
	cmd := lzt.cmdStream
	ci, cEnd := 0, lzt.cmdStreamSize
	lenStream := lzt.lenStream
	li := 0
	lit := lzt.litStream
	lii, litEnd := 0, lzt.litStreamSize
	offsStream := lzt.offsStream
	oi := 0

	var recentOffs [7]int32
	recentOffs[3] = -8
	recentOffs[4] = -8
	recentOffs[5] = -8
	lastOffset := int32(-8)

	for ci < cEnd {
		f := uint32(cmd.byteAt(ci))
		ci++
		litlen := int(f & 3)
		offsIndex := int(f >> 6)
		matchlen := uint32((f >> 2) & 0xF)

		if litlen == 3 {
			if li < len(lenStream) {
				litlen = int(lenStream[li])
			} else {
				litlen = 0
			}
			li++
		}
		if oi < len(offsStream) {
			recentOffs[6] = offsStream[oi]
		} else {
			recentOffs[6] = 0
		}

		copy64Add(o, dst, lit.buf, lit.i+lii, dst+int(lastOffset))
		if litlen > 8 {
			copy64Add(o, dst+8, lit.buf, lit.i+lii+8, dst+int(lastOffset)+8)
			if litlen > 16 {
				copy64Add(o, dst+16, lit.buf, lit.i+lii+16, dst+int(lastOffset)+16)
				for litlen > 24 {
					copy64Add(o, dst+24, lit.buf, lit.i+lii+24, dst+int(lastOffset)+24)
					litlen -= 8
					dst += 8
					lii += 8
				}
			}
		}
		dst += litlen
		lii += litlen

		offset := recentOffs[offsIndex+3]
		recentOffs[offsIndex+3] = recentOffs[offsIndex+2]
		recentOffs[offsIndex+2] = recentOffs[offsIndex+1]
		recentOffs[offsIndex+1] = recentOffs[offsIndex+0]
		recentOffs[3] = offset
		lastOffset = offset

		if (offsIndex+1)&4 != 0 {
			oi++
		}

		if int(offset) < dstStart-dst {
			return false
		}

		copyfrom := dst + int(offset)
		if matchlen != 15 {
			copy64(o, dst, o, copyfrom)
			copy64(o, dst+8, o, copyfrom+8)
			dst += int(matchlen) + 2
		} else {
			if li >= len(lenStream) {
				return false
			}
			matchlen = 14 + uint32(lenStream[li])
			li++
			if uint64(matchlen) > uint64(int64(dstEnd-dst)) {
				return false
			}
			copy64(o, dst, o, copyfrom)
			copy64(o, dst+8, o, copyfrom+8)
			copy64(o, dst+16, o, copyfrom+16)
			for {
				copy64(o, dst+24, o, copyfrom+24)
				matchlen -= 8
				dst += 8
				copyfrom += 8
				if matchlen <= 24 {
					break
				}
			}
			dst += int(matchlen)
		}
	}

	if oi != len(offsStream) || li != len(lenStream) {
		return false
	}

	finalLen := dstEnd - dst
	if finalLen != litEnd-lii {
		return false
	}

	for finalLen >= 8 {
		copy64Add(o, dst, lit.buf, lit.i+lii, dst+int(lastOffset))
		dst += 8
		lii += 8
		finalLen -= 8
	}
	oc := o
	for ; finalLen > 0; finalLen-- {
		oc[dst] = lit.byteAt(lii) + oc[dst+int(lastOffset)]
		lii++
		dst++
	}
	return true
}

// krakenProcessLzRunsType1 is the raw literal variant.
func krakenProcessLzRunsType1(lzt *krakenLzTable, o []byte, dst, dstEnd, dstStart int) bool {
	cmd := lzt.cmdStream
	ci, cEnd := 0, lzt.cmdStreamSize
	lenStream := lzt.lenStream
	li := 0
	lit := lzt.litStream
	lii, litEnd := 0, lzt.litStreamSize
	offsStream := lzt.offsStream
	oi := 0

	var recentOffs [7]int32
	recentOffs[3] = -8
	recentOffs[4] = -8
	recentOffs[5] = -8

	for ci < cEnd {
		f := uint32(cmd.byteAt(ci))
		ci++
		litlen := int(f & 3)
		offsIndex := int(f >> 6)
		matchlen := uint32((f >> 2) & 0xF)

		if litlen == 3 {
			if li < len(lenStream) {
				litlen = int(lenStream[li])
			} else {
				litlen = 0
			}
			li++
		}
		if oi < len(offsStream) {
			recentOffs[6] = offsStream[oi]
		} else {
			recentOffs[6] = 0
		}

		copy64(o, dst, lit.buf, lit.i+lii)
		if litlen > 8 {
			copy64(o, dst+8, lit.buf, lit.i+lii+8)
			if litlen > 16 {
				copy64(o, dst+16, lit.buf, lit.i+lii+16)
				for litlen > 24 {
					copy64(o, dst+24, lit.buf, lit.i+lii+24)
					litlen -= 8
					dst += 8
					lii += 8
				}
			}
		}
		dst += litlen
		lii += litlen

		offset := recentOffs[offsIndex+3]
		recentOffs[offsIndex+3] = recentOffs[offsIndex+2]
		recentOffs[offsIndex+2] = recentOffs[offsIndex+1]
		recentOffs[offsIndex+1] = recentOffs[offsIndex+0]
		recentOffs[3] = offset

		if (offsIndex+1)&4 != 0 {
			oi++
		}

		if int(offset) < dstStart-dst {
			return false
		}

		copyfrom := dst + int(offset)
		if matchlen != 15 {
			copy64(o, dst, o, copyfrom)
			copy64(o, dst+8, o, copyfrom+8)
			dst += int(matchlen) + 2
		} else {
			if li >= len(lenStream) {
				return false
			}
			matchlen = 14 + uint32(lenStream[li])
			li++
			if uint64(matchlen) > uint64(int64(dstEnd-dst)) {
				return false
			}
			copy64(o, dst, o, copyfrom)
			copy64(o, dst+8, o, copyfrom+8)
			copy64(o, dst+16, o, copyfrom+16)
			for {
				copy64(o, dst+24, o, copyfrom+24)
				matchlen -= 8
				dst += 8
				copyfrom += 8
				if matchlen <= 24 {
					break
				}
			}
			dst += int(matchlen)
		}
	}

	if oi != len(offsStream) || li != len(lenStream) {
		return false
	}

	finalLen := dstEnd - dst
	if finalLen != litEnd-lii {
		return false
	}

	for finalLen >= 64 {
		copy64Bytes(o, dst, lit.buf, lit.i+lii)
		dst += 64
		lii += 64
		finalLen -= 64
	}
	for finalLen >= 8 {
		copy64(o, dst, lit.buf, lit.i+lii)
		dst += 8
		lii += 8
		finalLen -= 8
	}
	oc := o
	for ; finalLen > 0; finalLen-- {
		oc[dst] = lit.byteAt(lii)
		lii++
		dst++
	}
	return true
}

func krakenProcessLzRuns(mode int, o []byte, dst, dstSize, offset int, lzt *krakenLzTable) bool {
	dstEnd := dst + dstSize
	start := dst
	if offset == 0 {
		start += 8
	}
	switch mode {
	case 1:
		return krakenProcessLzRunsType1(lzt, o, start, dstEnd, dst-offset)
	case 0:
		return krakenProcessLzRunsType0(lzt, o, start, dstEnd, dst-offset)
	}
	return false
}

// krakenDecodeQuantum decodes one 256k quantum.
func krakenDecodeQuantum(o []byte, dst, dstEnd, dstStart int,
	s []byte, src, srcEnd int, scr scratch) int {
	srcIn := src

	for dstEnd-dst != 0 {
		dstCount := dstEnd - dst
		if dstCount > 0x20000 {
			dstCount = 0x20000
		}
		if srcEnd-src < 4 {
			return -1
		}
		chunkhdr := int(rd24BE(s, src))
		if chunkhdr&0x800000 == 0 {
			// Entropy only, no match copying.
			srcUsed, _, writtenBytes := krakenDecodeBytes(s, src, srcEnd, o, dst, dstCount, false, scr)
			if srcUsed < 0 || writtenBytes != dstCount {
				return -1
			}
			src += srcUsed
		} else {
			src += 3
			srcUsed := chunkhdr & 0x7FFFF
			mode := (chunkhdr >> 19) & 0xF
			if srcEnd-src < srcUsed {
				return -1
			}
			if srcUsed < dstCount {
				scratchUsage := minInt(minInt(3*dstCount+32+0xd000, 0x6C000), scr.end-scr.cur)
				if scratchUsage < sizeofKrakenLzTable {
					return -1
				}
				inner := scr.window(scr.cur+sizeofKrakenLzTable, scr.cur+scratchUsage)
				var lzt krakenLzTable
				if !krakenReadLzTable(mode, s, src, src+srcUsed, o, dst, dstCount, dst-dstStart, inner, &lzt) {
					return -1
				}
				if !krakenProcessLzRuns(mode, o, dst, dstCount, dst-dstStart, &lzt) {
					return -1
				}
			} else if srcUsed > dstCount || mode != 0 {
				return -1
			} else {
				copy(o[dst:dst+dstCount], s[src:src+dstCount])
			}
			src += srcUsed
		}
		dst += dstCount
	}
	return src - srcIn
}
