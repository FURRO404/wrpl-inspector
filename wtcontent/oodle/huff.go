package oodle

import "math/bits"

type huffRevLut struct {
	bits2len [2048]byte
	bits2sym [2048]byte
}

type newHuffLut struct {
	bits2len [2048 + 16]byte
	bits2sym [2048 + 16]byte
}

// rev11 maps an 11 bit index to the same index with its bits reversed.
var rev11 = func() [2048]uint16 {
	var t [2048]uint16
	for i := range t {
		t[i] = bits.Reverse16(uint16(i)) >> 5
	}
	return t
}()

// reverseBitsLut permutes both halves of a Huffman lookup table so that a
// bit pattern read least significant bit first indexes it directly.
func reverseBitsLut(src *newHuffLut, dst *huffRevLut) {
	for i := 0; i < 2048; i++ {
		j := rev11[i]
		dst.bits2len[j] = src.bits2len[i]
		dst.bits2sym[j] = src.bits2sym[i]
	}
}

// huffReader decodes three interleaved Huffman streams: src and srcMid run
// forwards, srcEnd runs backwards.
type huffReader struct {
	out  []byte
	o    int
	oEnd int

	s         []byte
	src       int
	srcMid    int
	srcEnd    int
	srcMidOrg int

	srcBitpos    int
	srcMidBitpos int
	srcEndBitpos int
	srcBits      uint32
	srcMidBits   uint32
	srcEndBits   uint32
}

func decodeBytesCore(hr *huffReader, lut *huffRevLut) bool {
	s := hr.s
	src, srcBits, srcBitpos := hr.src, hr.srcBits, hr.srcBitpos
	srcMid, srcMidBits, srcMidBitpos := hr.srcMid, hr.srcMidBits, hr.srcMidBitpos
	srcEnd, srcEndBits, srcEndBitpos := hr.srcEnd, hr.srcEndBits, hr.srcEndBitpos

	dst, dstEnd := hr.o, hr.oEnd
	out := hr.out

	if src > srcMid {
		return false
	}

	if hr.srcEnd-srcMid >= 4 && dstEnd-dst >= 6 {
		dstEnd -= 5
		srcEnd -= 4

		for dst < dstEnd && src <= srcMid && srcMid <= srcEnd {
			srcBits |= rd32(s, src) << uint(srcBitpos)
			src += (31 - srcBitpos) >> 3

			srcEndBits |= rdbe32(s, srcEnd) << uint(srcEndBitpos)
			srcEnd -= (31 - srcEndBitpos) >> 3

			srcMidBits |= rd32(s, srcMid) << uint(srcMidBitpos)
			srcMid += (31 - srcMidBitpos) >> 3

			srcBitpos |= 0x18
			srcEndBitpos |= 0x18
			srcMidBitpos |= 0x18

			k := srcBits & 0x7FF
			n := lut.bits2len[k]
			srcBits >>= n
			srcBitpos -= int(n)
			out[dst+0] = lut.bits2sym[k]

			k = srcEndBits & 0x7FF
			n = lut.bits2len[k]
			srcEndBits >>= n
			srcEndBitpos -= int(n)
			out[dst+1] = lut.bits2sym[k]

			k = srcMidBits & 0x7FF
			n = lut.bits2len[k]
			srcMidBits >>= n
			srcMidBitpos -= int(n)
			out[dst+2] = lut.bits2sym[k]

			k = srcBits & 0x7FF
			n = lut.bits2len[k]
			srcBits >>= n
			srcBitpos -= int(n)
			out[dst+3] = lut.bits2sym[k]

			k = srcEndBits & 0x7FF
			n = lut.bits2len[k]
			srcEndBits >>= n
			srcEndBitpos -= int(n)
			out[dst+4] = lut.bits2sym[k]

			k = srcMidBits & 0x7FF
			n = lut.bits2len[k]
			srcMidBits >>= n
			srcMidBitpos -= int(n)
			out[dst+5] = lut.bits2sym[k]
			dst += 6
		}
		dstEnd += 5

		src -= srcBitpos >> 3
		srcBitpos &= 7

		srcEnd += 4 + (srcEndBitpos >> 3)
		srcEndBitpos &= 7

		srcMid -= srcMidBitpos >> 3
		srcMidBitpos &= 7
	}

	for {
		if dst >= dstEnd {
			break
		}

		if srcMid-src <= 1 {
			if srcMid-src == 1 {
				srcBits |= uint32(at(s, src)) << uint(srcBitpos)
			}
		} else {
			srcBits |= uint32(rd16(s, src)) << uint(srcBitpos)
		}
		k := srcBits & 0x7FF
		n := lut.bits2len[k]
		srcBitpos -= int(n)
		srcBits >>= n
		out[dst] = lut.bits2sym[k]
		dst++
		src += (7 - srcBitpos) >> 3
		srcBitpos &= 7

		if dst < dstEnd {
			if srcEnd-srcMid <= 1 {
				if srcEnd-srcMid == 1 {
					srcEndBits |= uint32(at(s, srcMid)) << uint(srcEndBitpos)
					srcMidBits |= uint32(at(s, srcMid)) << uint(srcMidBitpos)
				}
			} else {
				v := uint32(rd16(s, srcEnd-2))
				srcEndBits |= (((v >> 8) | (v << 8)) & 0xffff) << uint(srcEndBitpos)
				srcMidBits |= uint32(rd16(s, srcMid)) << uint(srcMidBitpos)
			}
			n = lut.bits2len[srcEndBits&0x7FF]
			out[dst] = lut.bits2sym[srcEndBits&0x7FF]
			dst++
			srcEndBitpos -= int(n)
			srcEndBits >>= n
			srcEnd -= (7 - srcEndBitpos) >> 3
			srcEndBitpos &= 7
			if dst < dstEnd {
				n = lut.bits2len[srcMidBits&0x7FF]
				out[dst] = lut.bits2sym[srcMidBits&0x7FF]
				dst++
				srcMidBitpos -= int(n)
				srcMidBits >>= n
				srcMid += (7 - srcMidBitpos) >> 3
				srcMidBitpos &= 7
			}
		}
		if src > srcMid || srcMid > srcEnd {
			return false
		}
	}
	return src == hr.srcMidOrg && srcEnd == srcMid
}

func huffReadCodeLengthsOld(br *bitReader, syms []byte, codePrefix *[12]uint32) int {
	if br.readBitNoRefill() != 0 {
		sym, numSymbols := 0, 0
		avgBitsX4 := 32
		forcedBits := br.readBitsNoRefill(2)

		thresForValidGammaBits := uint32(1) << uint(31-(20>>uint(forcedBits)))
		skipInitialZeros := br.readBit() != 0
		for {
			if !skipInitialZeros {
				if br.bits&0xff000000 == 0 {
					return -1
				}
				sym += br.readBitsNoRefill(2*(clz(br.bits)+1)) - 2 + 1
				if sym >= 256 {
					break
				}
			}
			skipInitialZeros = false

			br.refill()
			if br.bits&0xff000000 == 0 {
				return -1
			}
			n := br.readBitsNoRefill(2*(clz(br.bits)+1)) - 2 + 1
			if sym+n > 256 {
				return -1
			}
			br.refill()
			numSymbols += n
			for {
				if br.bits < thresForValidGammaBits {
					return -1
				}
				lz := clz(br.bits)
				v := br.readBitsNoRefill(lz+forcedBits+1) + ((lz - 1) << uint(forcedBits))
				codelen := (-(v & 1) ^ (v >> 1)) + ((avgBitsX4 + 2) >> 2)
				if codelen < 1 || codelen > 11 {
					return -1
				}
				avgBitsX4 = codelen + ((3*avgBitsX4 + 2) >> 2)
				br.refill()
				syms[codePrefix[codelen]] = byte(sym)
				codePrefix[codelen]++
				sym++
				n--
				if n == 0 {
					break
				}
			}
			if sym == 256 {
				break
			}
		}
		if sym == 256 && numSymbols >= 2 {
			return numSymbols
		}
		return -1
	}

	// Sparse symbol encoding.
	numSymbols := br.readBitsNoRefill(8)
	if numSymbols == 0 {
		return -1
	}
	if numSymbols == 1 {
		syms[0] = byte(br.readBitsNoRefill(8))
	} else {
		codelenBits := br.readBitsNoRefill(3)
		if codelenBits > 4 {
			return -1
		}
		for i := 0; i < numSymbols; i++ {
			br.refill()
			sym := br.readBitsNoRefill(8)
			codelen := br.readBitsNoRefillZero(codelenBits) + 1
			if codelen > 11 {
				return -1
			}
			syms[codePrefix[codelen]] = byte(sym)
			codePrefix[codelen]++
		}
	}
	return numSymbols
}

var kRiceCodeBits2Value = [256]uint32{
	0x80000000, 0x00000007, 0x10000006, 0x00000006, 0x20000005, 0x00000105, 0x10000005, 0x00000005,
	0x30000004, 0x00000204, 0x10000104, 0x00000104, 0x20000004, 0x00010004, 0x10000004, 0x00000004,
	0x40000003, 0x00000303, 0x10000203, 0x00000203, 0x20000103, 0x00010103, 0x10000103, 0x00000103,
	0x30000003, 0x00020003, 0x10010003, 0x00010003, 0x20000003, 0x01000003, 0x10000003, 0x00000003,
	0x50000002, 0x00000402, 0x10000302, 0x00000302, 0x20000202, 0x00010202, 0x10000202, 0x00000202,
	0x30000102, 0x00020102, 0x10010102, 0x00010102, 0x20000102, 0x01000102, 0x10000102, 0x00000102,
	0x40000002, 0x00030002, 0x10020002, 0x00020002, 0x20010002, 0x01010002, 0x10010002, 0x00010002,
	0x30000002, 0x02000002, 0x11000002, 0x01000002, 0x20000002, 0x00000012, 0x10000002, 0x00000002,
	0x60000001, 0x00000501, 0x10000401, 0x00000401, 0x20000301, 0x00010301, 0x10000301, 0x00000301,
	0x30000201, 0x00020201, 0x10010201, 0x00010201, 0x20000201, 0x01000201, 0x10000201, 0x00000201,
	0x40000101, 0x00030101, 0x10020101, 0x00020101, 0x20010101, 0x01010101, 0x10010101, 0x00010101,
	0x30000101, 0x02000101, 0x11000101, 0x01000101, 0x20000101, 0x00000111, 0x10000101, 0x00000101,
	0x50000001, 0x00040001, 0x10030001, 0x00030001, 0x20020001, 0x01020001, 0x10020001, 0x00020001,
	0x30010001, 0x02010001, 0x11010001, 0x01010001, 0x20010001, 0x00010011, 0x10010001, 0x00010001,
	0x40000001, 0x03000001, 0x12000001, 0x02000001, 0x21000001, 0x01000011, 0x11000001, 0x01000001,
	0x30000001, 0x00000021, 0x10000011, 0x00000011, 0x20000001, 0x00001001, 0x10000001, 0x00000001,
	0x70000000, 0x00000600, 0x10000500, 0x00000500, 0x20000400, 0x00010400, 0x10000400, 0x00000400,
	0x30000300, 0x00020300, 0x10010300, 0x00010300, 0x20000300, 0x01000300, 0x10000300, 0x00000300,
	0x40000200, 0x00030200, 0x10020200, 0x00020200, 0x20010200, 0x01010200, 0x10010200, 0x00010200,
	0x30000200, 0x02000200, 0x11000200, 0x01000200, 0x20000200, 0x00000210, 0x10000200, 0x00000200,
	0x50000100, 0x00040100, 0x10030100, 0x00030100, 0x20020100, 0x01020100, 0x10020100, 0x00020100,
	0x30010100, 0x02010100, 0x11010100, 0x01010100, 0x20010100, 0x00010110, 0x10010100, 0x00010100,
	0x40000100, 0x03000100, 0x12000100, 0x02000100, 0x21000100, 0x01000110, 0x11000100, 0x01000100,
	0x30000100, 0x00000120, 0x10000110, 0x00000110, 0x20000100, 0x00001100, 0x10000100, 0x00000100,
	0x60000000, 0x00050000, 0x10040000, 0x00040000, 0x20030000, 0x01030000, 0x10030000, 0x00030000,
	0x30020000, 0x02020000, 0x11020000, 0x01020000, 0x20020000, 0x00020010, 0x10020000, 0x00020000,
	0x40010000, 0x03010000, 0x12010000, 0x02010000, 0x21010000, 0x01010010, 0x11010000, 0x01010000,
	0x30010000, 0x00010020, 0x10010010, 0x00010010, 0x20010000, 0x00011000, 0x10010000, 0x00010000,
	0x50000000, 0x04000000, 0x13000000, 0x03000000, 0x22000000, 0x02000010, 0x12000000, 0x02000000,
	0x31000000, 0x01000020, 0x11000010, 0x01000010, 0x21000000, 0x01001000, 0x11000000, 0x01000000,
	0x40000000, 0x00000030, 0x10000020, 0x00000020, 0x20000010, 0x00001010, 0x10000010, 0x00000010,
	0x30000000, 0x00002000, 0x10001000, 0x00001000, 0x20000000, 0x00100000, 0x10000000, 0x00000000,
}

var kRiceCodeBits2Len = [256]uint8{
	0, 1, 1, 2, 1, 2, 2, 3, 1, 2, 2, 3, 2, 3, 3, 4, 1, 2, 2, 3, 2, 3, 3, 4, 2, 3, 3, 4, 3, 4, 4, 5,
	1, 2, 2, 3, 2, 3, 3, 4, 2, 3, 3, 4, 3, 4, 4, 5, 2, 3, 3, 4, 3, 4, 4, 5, 3, 4, 4, 5, 4, 5, 5, 6,
	1, 2, 2, 3, 2, 3, 3, 4, 2, 3, 3, 4, 3, 4, 4, 5, 2, 3, 3, 4, 3, 4, 4, 5, 3, 4, 4, 5, 4, 5, 5, 6,
	2, 3, 3, 4, 3, 4, 4, 5, 3, 4, 4, 5, 4, 5, 5, 6, 3, 4, 4, 5, 4, 5, 5, 6, 4, 5, 5, 6, 5, 6, 6, 7,
	1, 2, 2, 3, 2, 3, 3, 4, 2, 3, 3, 4, 3, 4, 4, 5, 2, 3, 3, 4, 3, 4, 4, 5, 3, 4, 4, 5, 4, 5, 5, 6,
	2, 3, 3, 4, 3, 4, 4, 5, 3, 4, 4, 5, 4, 5, 5, 6, 3, 4, 4, 5, 4, 5, 5, 6, 4, 5, 5, 6, 5, 6, 6, 7,
	2, 3, 3, 4, 3, 4, 4, 5, 3, 4, 4, 5, 4, 5, 5, 6, 3, 4, 4, 5, 4, 5, 5, 6, 4, 5, 5, 6, 5, 6, 6, 7,
	3, 4, 4, 5, 4, 5, 5, 6, 4, 5, 5, 6, 5, 6, 6, 7, 4, 5, 5, 6, 5, 6, 6, 7, 5, 6, 6, 7, 6, 7, 7, 8,
}

// decodeGolombRiceLengths writes into dst, which must have 8 bytes of slack
// after size.
func decodeGolombRiceLengths(dst []byte, size int, br *bitReader2) bool {
	p, pEnd := br.p, br.pEnd
	di, dstEnd := 0, size
	if p >= pEnd {
		return false
	}

	count := int32(-int32(br.bitpos))
	v := uint32(at(br.s, p)) & (255 >> br.bitpos)
	p++
	for {
		if v == 0 {
			count += 8
		} else {
			x := kRiceCodeBits2Value[v]
			wr32(dst, di+0, uint32(count)+(x&0x0f0f0f0f))
			wr32(dst, di+4, (x>>4)&0x0f0f0f0f)
			di += int(kRiceCodeBits2Len[v])
			if di >= dstEnd {
				break
			}
			count = int32(x >> 28)
		}
		if p >= pEnd {
			return false
		}
		v = uint32(at(br.s, p))
		p++
	}
	// Went too far, step back.
	if di > dstEnd {
		for n := di - dstEnd; n > 0; n-- {
			v &= v - 1
		}
	}
	// Step back if the byte is not finished.
	bitpos := uint32(0)
	if v&1 == 0 {
		p--
		bitpos = uint32(8 - bsf(v))
	}
	br.p = p
	br.bitpos = bitpos
	return true
}

// decodeGolombRiceBits needs 8 bytes of slack after size in dst.
func decodeGolombRiceBits(dst []byte, size, bitcount int, br *bitReader2) bool {
	if bitcount == 0 {
		return true
	}
	di, dstEnd := 0, size
	p := br.p
	bitpos := int(br.bitpos)

	bitsRequired := bitpos + bitcount*size
	bytesRequired := (bitsRequired + 7) >> 3
	if bytesRequired > br.pEnd-p {
		return false
	}

	br.p = p + (bitsRequired >> 3)
	br.bitpos = uint32(bitsRequired & 7)

	bak := rd64(dst, dstEnd)

	switch {
	case bitcount < 2:
		for {
			b := uint64(uint8(rdbe32(br.s, p) >> uint(24-bitpos)))
			p++
			b = (b | (b << 28)) & 0xF0000000F
			b = (b | (b << 14)) & 0x3000300030003
			b = (b | (b << 7)) & 0x0101010101010101
			wr64(dst, di, rd64(dst, di)*2+bits.ReverseBytes64(b))
			di += 8
			if di >= dstEnd {
				break
			}
		}
	case bitcount == 2:
		for {
			b := uint64(uint16(rdbe32(br.s, p) >> uint(16-bitpos)))
			p += 2
			b = (b | (b << 24)) & 0xFF000000FF
			b = (b | (b << 12)) & 0xF000F000F000F
			b = (b | (b << 6)) & 0x0303030303030303
			wr64(dst, di, rd64(dst, di)*4+bits.ReverseBytes64(b))
			di += 8
			if di >= dstEnd {
				break
			}
		}
	default:
		for {
			b := uint64(rdbe32(br.s, p)>>uint(8-bitpos)) & 0xffffff
			p += 3
			b = (b | (b << 20)) & 0xFFF00000FFF
			b = (b | (b << 10)) & 0x3F003F003F003F
			b = (b | (b << 5)) & 0x0707070707070707
			wr64(dst, di, rd64(dst, di)*8+bits.ReverseBytes64(b))
			di += 8
			if di >= dstEnd {
				break
			}
		}
	}
	wr64(dst, dstEnd, bak)
	return true
}

type huffRange struct {
	symbol uint16
	num    uint16
}

func huffConvertToRanges(rng []huffRange, numSymbols, p int, symlen []byte, br *bitReader) int {
	numRanges := p >> 1
	symIdx := 0
	si := 0

	if p&1 != 0 {
		br.refill()
		v := int(symlen[si])
		si++
		if v >= 8 {
			return -1
		}
		symIdx = br.readBitsNoRefill(v+1) + (1 << uint(v+1)) - 1
	}
	symsUsed := 0

	for i := 0; i < numRanges; i++ {
		br.refill()
		v := int(symlen[si])
		if v >= 9 {
			return -1
		}
		num := br.readBitsNoRefillZero(v) + (1 << uint(v))
		v = int(symlen[si+1])
		if v >= 8 {
			return -1
		}
		space := br.readBitsNoRefill(v+1) + (1 << uint(v+1)) - 1
		rng[i].symbol = uint16(symIdx)
		rng[i].num = uint16(num)
		symsUsed += num
		symIdx += num + space
		si += 2
	}

	if symIdx >= 256 || symsUsed >= numSymbols || symIdx+numSymbols-symsUsed > 256 {
		return -1
	}

	rng[numRanges].symbol = uint16(symIdx)
	rng[numRanges].num = uint16(numSymbols - symsUsed)

	return numRanges + 1
}

func huffReadCodeLengthsNew(br *bitReader, syms []byte, codePrefix *[12]uint32) int {
	forcedBits := br.readBitsNoRefill(2)
	numSymbols := br.readBitsNoRefill(8) + 1
	fluff := br.readFluff(numSymbols)

	var codeLenBuf [512 + 16]byte
	codeLen := codeLenBuf[: 512 : 512+16]

	var br2 bitReader2
	br2.s = br.s
	br2.bitpos = uint32((br.bitpos - 24) & 7)
	br2.pEnd = br.pEnd
	br2.p = br.p - ((24-br.bitpos)+7)>>3

	if !decodeGolombRiceLengths(codeLen, numSymbols+fluff, &br2) {
		return -1
	}
	for i := numSymbols + fluff; i < numSymbols+fluff+16 && i < len(codeLenBuf); i++ {
		codeLenBuf[i] = 0
	}
	if !decodeGolombRiceBits(codeLen, numSymbols, forcedBits, &br2) {
		return -1
	}

	// Reset the bit decoder.
	br.bitpos = 24
	br.p = br2.p
	br.bits = 0
	br.refill()
	br.bits <<= br2.bitpos
	br.bitpos += int(br2.bitpos)

	runningSum := uint32(0x1e)
	for i := 0; i < numSymbols; i++ {
		v := int(codeLen[i])
		v = (-(v & 1)) ^ (v >> 1)
		codeLen[i] = byte(v + int(runningSum>>2) + 1)
		if codeLen[i] < 1 || codeLen[i] > 11 {
			return -1
		}
		runningSum += uint32(v)
	}

	var rng [128]huffRange
	ranges := huffConvertToRanges(rng[:], numSymbols, fluff, codeLen[numSymbols:], br)
	if ranges <= 0 {
		return -1
	}

	cp := 0
	for i := 0; i < ranges; i++ {
		sym := int(rng[i].symbol)
		for n := int(rng[i].num); n > 0; n-- {
			syms[codePrefix[codeLen[cp]]] = byte(sym)
			codePrefix[codeLen[cp]]++
			cp++
			sym++
		}
	}

	return numSymbols
}

func huffMakeLut(prefixOrg, prefixCur *[12]uint32, lut *newHuffLut, syms []byte) bool {
	currslot := uint32(0)
	for i := uint32(1); i < 11; i++ {
		start := prefixOrg[i]
		count := prefixCur[i] - start
		if count != 0 {
			stepsize := uint32(1) << (11 - i)
			numToSet := count << (11 - i)
			if currslot+numToSet > 2048 {
				return false
			}
			fillByte(lut.bits2len[:], int(currslot), byte(i), int(numToSet))
			p := currslot
			for j := uint32(0); j != count; j++ {
				fillByte(lut.bits2sym[:], int(p), syms[start+j], int(stepsize))
				p += stepsize
			}
			currslot += numToSet
		}
	}
	if prefixCur[11]-prefixOrg[11] != 0 {
		numToSet := prefixCur[11] - prefixOrg[11]
		if currslot+numToSet > 2048 {
			return false
		}
		fillByte(lut.bits2len[:], int(currslot), 11, int(numToSet))
		copy(lut.bits2sym[currslot:], syms[prefixOrg[11]:prefixOrg[11]+numToSet])
		currslot += numToSet
	}
	return currslot == 2048
}

var codePrefixOrg = [12]uint32{0x0, 0x0, 0x2, 0x6, 0xE, 0x1E, 0x3E, 0x7E, 0xFE, 0x1FE, 0x2FE, 0x3FE}

// decodeBytesType12 decodes a Huffman coded chunk. src is the chunk, out the
// destination window.
func decodeBytesType12(s []byte, src, srcSize int, out []byte, o, outputSize, typ int) int {
	srcEnd := src + srcSize

	var br bitReader
	br.s = s
	br.bitpos = 24
	br.bits = 0
	br.p = src
	br.pEnd = srcEnd
	br.refill()

	codePrefix := codePrefixOrg
	var syms [1280]byte
	var numSyms int
	if br.readBitNoRefill() == 0 {
		numSyms = huffReadCodeLengthsOld(&br, syms[:], &codePrefix)
	} else if br.readBitNoRefill() == 0 {
		numSyms = huffReadCodeLengthsNew(&br, syms[:], &codePrefix)
	} else {
		return -1
	}

	if numSyms < 1 {
		return -1
	}
	src = br.p - (24-br.bitpos)/8

	if numSyms == 1 {
		fillByte(out, o, syms[0], outputSize)
		return src - srcEnd
	}

	var huffLut newHuffLut
	if !huffMakeLut(&codePrefixOrg, &codePrefix, &huffLut, syms[:]) {
		return -1
	}

	var revLut huffRevLut
	reverseBitsLut(&huffLut, &revLut)

	var hr huffReader
	hr.s = s
	hr.out = out

	if typ == 1 {
		if src+3 > srcEnd {
			return -1
		}
		splitMid := int(rd16(s, src))
		src += 2
		hr.o = o
		hr.oEnd = o + outputSize
		hr.src = src
		hr.srcEnd = srcEnd
		hr.srcMid = src + splitMid
		hr.srcMidOrg = hr.srcMid
		hr.srcBitpos, hr.srcBits = 0, 0
		hr.srcMidBitpos, hr.srcMidBits = 0, 0
		hr.srcEndBitpos, hr.srcEndBits = 0, 0
		if !decodeBytesCore(&hr, &revLut) {
			return -1
		}
	} else {
		if src+6 > srcEnd {
			return -1
		}

		halfOutputSize := (outputSize + 1) >> 1
		splitMid := int(rd32(s, src) & 0xFFFFFF)
		src += 3
		if splitMid > srcEnd-src {
			return -1
		}
		srcMid := src + splitMid
		splitLeft := int(rd16(s, src))
		src += 2
		if srcMid-src < splitLeft+2 || srcEnd-srcMid < 3 {
			return -1
		}
		splitRight := int(rd16(s, srcMid))
		if srcEnd-(srcMid+2) < splitRight+2 {
			return -1
		}

		hr.o = o
		hr.oEnd = o + halfOutputSize
		hr.src = src
		hr.srcEnd = srcMid
		hr.srcMid = src + splitLeft
		hr.srcMidOrg = hr.srcMid
		hr.srcBitpos, hr.srcBits = 0, 0
		hr.srcMidBitpos, hr.srcMidBits = 0, 0
		hr.srcEndBitpos, hr.srcEndBits = 0, 0
		if !decodeBytesCore(&hr, &revLut) {
			return -1
		}

		hr.o = o + halfOutputSize
		hr.oEnd = o + outputSize
		hr.src = srcMid + 2
		hr.srcEnd = srcEnd
		hr.srcMid = srcMid + 2 + splitRight
		hr.srcMidOrg = hr.srcMid
		hr.srcBitpos, hr.srcBits = 0, 0
		hr.srcMidBitpos, hr.srcMidBits = 0, 0
		hr.srcEndBitpos, hr.srcEndBits = 0, 0
		if !decodeBytesCore(&hr, &revLut) {
			return -1
		}
	}
	return srcSize
}
