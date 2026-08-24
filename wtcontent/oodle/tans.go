package oodle

type tansData struct {
	aUsed uint32
	bUsed uint32
	a     [256]byte
	b     [256]uint32
}

func simpleSortU8(p []byte) {
	for i := 1; i < len(p); i++ {
		t := p[i]
		j := i
		for j > 0 && t < p[j-1] {
			p[j] = p[j-1]
			j--
		}
		p[j] = t
	}
}

func simpleSortU32(p []uint32) {
	for i := 1; i < len(p); i++ {
		t := p[i]
		j := i
		for j > 0 && t < p[j-1] {
			p[j] = p[j-1]
			j--
		}
		p[j] = t
	}
}

func tansDecodeTable(br *bitReader, lBits int, td *tansData) bool {
	br.refill()
	if br.readBitNoRefill() != 0 {
		q := br.readBitsNoRefill(3)
		numSymbols := br.readBitsNoRefill(8) + 1
		if numSymbols < 2 {
			return false
		}
		fluff := br.readFluff(numSymbols)
		totalRiceValues := fluff + numSymbols
		var riceBuf [512 + 16]byte
		rice := riceBuf[: 512 : 512+16]

		var br2 bitReader2
		br2.s = br.s
		br2.p = br.p - ((24-br.bitpos)+7)>>3
		br2.pEnd = br.pEnd
		br2.bitpos = uint32((br.bitpos - 24) & 7)

		if !decodeGolombRiceLengths(rice, totalRiceValues, &br2) {
			return false
		}
		for i := totalRiceValues; i < totalRiceValues+16 && i < len(riceBuf); i++ {
			riceBuf[i] = 0
		}

		br.bitpos = 24
		br.p = br2.p
		br.bits = 0
		br.refill()
		br.bits <<= br2.bitpos
		br.bitpos += int(br2.bitpos)

		var rng [133]huffRange
		fluff = huffConvertToRanges(rng[:], numSymbols, fluff, rice[numSymbols:], br)
		if fluff < 0 {
			return false
		}

		br.refill()

		l := 1 << uint(lBits)
		curRice := 0
		average := 6
		somesum := 0
		ai, bi := 0, 0

		for ri := 0; ri < fluff; ri++ {
			symbol := int(rng[ri].symbol)
			for num := int(rng[ri].num); num > 0; num-- {
				br.refill()

				nextra := q + int(rice[curRice])
				curRice++
				if nextra > 15 {
					return false
				}
				v := br.readBitsNoRefillZero(nextra) + (1 << uint(nextra)) - (1 << uint(q))

				averageDiv4 := average >> 2
				limit := 2 * averageDiv4
				if v <= limit {
					v = averageDiv4 + ((-(v & 1)) ^ int(uint32(v)>>1))
				}
				if limit > v {
					limit = v
				}
				v++
				average += limit - averageDiv4
				td.a[ai] = byte(symbol)
				td.b[bi] = uint32(symbol<<16) + uint32(v)
				if v == 1 {
					ai++
				}
				if v >= 2 {
					bi++
				}
				somesum += v
				symbol++
			}
		}
		td.aUsed = uint32(ai)
		td.bUsed = uint32(bi)
		if somesum != l {
			return false
		}
		return true
	}

	var seen [256]bool
	l := 1 << uint(lBits)

	count := br.readBitsNoRefill(3) + 1

	bitsPerSym := bsr(uint32(lBits)) + 1
	maxDeltaBits := br.readBitsNoRefill(bitsPerSym)

	if maxDeltaBits == 0 || maxDeltaBits > lBits {
		return false
	}

	ai, bi := 0, 0
	weight := 0
	totalWeights := 0

	for ; count > 0; count-- {
		br.refill()

		sym := br.readBitsNoRefill(8)
		if seen[sym] {
			return false
		}

		delta := br.readBitsNoRefill(maxDeltaBits)
		weight += delta
		if weight == 0 {
			return false
		}

		seen[sym] = true
		if weight == 1 {
			td.a[ai] = byte(sym)
			ai++
		} else {
			td.b[bi] = uint32(sym<<16) + uint32(weight)
			bi++
		}
		totalWeights += weight
	}

	br.refill()

	sym := br.readBitsNoRefill(8)
	if seen[sym] {
		return false
	}

	if l-totalWeights < weight || l-totalWeights <= 1 {
		return false
	}

	td.b[bi] = uint32(sym<<16) + uint32(l-totalWeights)
	bi++

	td.aUsed = uint32(ai)
	td.bUsed = uint32(bi)

	simpleSortU8(td.a[:ai])
	simpleSortU32(td.b[:bi])
	return true
}

type tansLutEnt struct {
	x      uint32
	bitsX  uint8
	symbol uint8
	w      uint16
}

func tansInitLut(td *tansData, lBits int, lut []tansLutEnt) {
	var pointers [4]int

	l := 1 << uint(lBits)
	aUsed := int(td.aUsed)

	slotsLeftToAlloc := l - aUsed

	sa := slotsLeftToAlloc >> 2
	pointers[0] = 0
	sb := sa
	if slotsLeftToAlloc&3 > 0 {
		sb++
	}
	pointers[1] = sb
	sb += sa
	if slotsLeftToAlloc&3 > 1 {
		sb++
	}
	pointers[2] = sb
	sb += sa
	if slotsLeftToAlloc&3 > 2 {
		sb++
	}
	pointers[3] = sb

	// Entries with weight == 1.
	{
		base := slotsLeftToAlloc
		var le tansLutEnt
		le.w = 0
		le.bitsX = uint8(lBits)
		le.x = uint32(1<<uint(lBits)) - 1
		for i := 0; i < aUsed; i++ {
			lut[base+i] = le
			lut[base+i].symbol = td.a[i]
		}
	}

	// Entries with weight >= 2.
	weightsSum := 0
	for i := 0; i < int(td.bUsed); i++ {
		weight := int(td.b[i] & 0xffff)
		symbol := int(td.b[i] >> 16)
		if weight > 4 {
			symBits := bsr(uint32(weight))
			z := lBits - symBits
			var le tansLutEnt
			le.symbol = uint8(symbol)
			le.bitsX = uint8(z)
			le.x = uint32(1<<uint(z)) - 1
			le.w = uint16((l - 1) & (weight << uint(z)))
			whatToAdd := 1 << uint(z)
			x := (1 << uint(symBits+1)) - weight

			for j := 0; j < 4; j++ {
				dst := pointers[j]

				y := (weight + ((weightsSum - j - 1) & 3)) >> 2
				if x >= y {
					for n := y; n > 0; n-- {
						lut[dst] = le
						dst++
						le.w += uint16(whatToAdd)
					}
					x -= y
				} else {
					for n := x; n > 0; n-- {
						lut[dst] = le
						dst++
						le.w += uint16(whatToAdd)
					}
					z--
					whatToAdd >>= 1
					le.bitsX = uint8(z)
					le.w = 0
					le.x >>= 1
					for n := y - x; n > 0; n-- {
						lut[dst] = le
						dst++
						le.w += uint16(whatToAdd)
					}
					x = weight
				}
				pointers[j] = dst
			}
		} else {
			b := uint32((1<<uint(weight))-1) << uint(weightsSum&3)
			b |= b >> 4
			ww := weight
			for n := weight; n > 0; n-- {
				idx := bsf(b)
				b &= b - 1
				dst := pointers[idx]
				pointers[idx]++
				lut[dst].symbol = uint8(symbol)
				weightBits := bsr(uint32(ww))
				lut[dst].bitsX = uint8(lBits - weightBits)
				lut[dst].x = uint32(1<<uint(lBits-weightBits)) - 1
				lut[dst].w = uint16((l - 1) & (ww << uint(lBits-weightBits)))
				ww++
			}
		}
		weightsSum += weight
	}
}

type tansDecoderParams struct {
	lut          []tansLutEnt
	out          []byte
	dst, dstEnd  int
	s            []byte
	ptrF, ptrB   int
	bitsF, bitsB uint32
	bitposF      int
	bitposB      int
	state        [5]uint32
}

// tansDecode runs the five interleaved TANS states, three forwards and then
// three backwards, until the output is full. It is written out in full rather
// than looped so the per symbol work stays in registers.
func tansDecode(p *tansDecoderParams) bool {
	lut := p.lut
	out := p.out
	dst, dstEnd := p.dst, p.dstEnd
	s := p.s
	ptrF, ptrB := p.ptrF, p.ptrB
	bitsF, bitsB := p.bitsF, p.bitsB
	bitposF, bitposB := p.bitposF, p.bitposB
	st := p.state
	var e *tansLutEnt

	if ptrF > ptrB {
		return false
	}

	if dst < dstEnd {
	decodeLoop:
		for {
			bitsF |= rd32(s, ptrF) << uint(bitposF)
			ptrF += (31 - bitposF) >> 3
			bitposF |= 24
			e = &lut[st[0]]
			out[dst] = e.symbol
			dst++
			bitposF -= int(e.bitsX)
			st[0] = (bitsF & e.x) + uint32(e.w)
			bitsF >>= e.bitsX
			if dst >= dstEnd {
				break decodeLoop
			}
			e = &lut[st[1]]
			out[dst] = e.symbol
			dst++
			bitposF -= int(e.bitsX)
			st[1] = (bitsF & e.x) + uint32(e.w)
			bitsF >>= e.bitsX
			if dst >= dstEnd {
				break decodeLoop
			}
			bitsF |= rd32(s, ptrF) << uint(bitposF)
			ptrF += (31 - bitposF) >> 3
			bitposF |= 24
			e = &lut[st[2]]
			out[dst] = e.symbol
			dst++
			bitposF -= int(e.bitsX)
			st[2] = (bitsF & e.x) + uint32(e.w)
			bitsF >>= e.bitsX
			if dst >= dstEnd {
				break decodeLoop
			}
			e = &lut[st[3]]
			out[dst] = e.symbol
			dst++
			bitposF -= int(e.bitsX)
			st[3] = (bitsF & e.x) + uint32(e.w)
			bitsF >>= e.bitsX
			if dst >= dstEnd {
				break decodeLoop
			}
			bitsF |= rd32(s, ptrF) << uint(bitposF)
			ptrF += (31 - bitposF) >> 3
			bitposF |= 24
			e = &lut[st[4]]
			out[dst] = e.symbol
			dst++
			bitposF -= int(e.bitsX)
			st[4] = (bitsF & e.x) + uint32(e.w)
			bitsF >>= e.bitsX
			if dst >= dstEnd {
				break decodeLoop
			}
			bitsB |= rdbe32(s, ptrB-4) << uint(bitposB)
			ptrB -= (31 - bitposB) >> 3
			bitposB |= 24
			e = &lut[st[0]]
			out[dst] = e.symbol
			dst++
			bitposB -= int(e.bitsX)
			st[0] = (bitsB & e.x) + uint32(e.w)
			bitsB >>= e.bitsX
			if dst >= dstEnd {
				break decodeLoop
			}
			e = &lut[st[1]]
			out[dst] = e.symbol
			dst++
			bitposB -= int(e.bitsX)
			st[1] = (bitsB & e.x) + uint32(e.w)
			bitsB >>= e.bitsX
			if dst >= dstEnd {
				break decodeLoop
			}
			bitsB |= rdbe32(s, ptrB-4) << uint(bitposB)
			ptrB -= (31 - bitposB) >> 3
			bitposB |= 24
			e = &lut[st[2]]
			out[dst] = e.symbol
			dst++
			bitposB -= int(e.bitsX)
			st[2] = (bitsB & e.x) + uint32(e.w)
			bitsB >>= e.bitsX
			if dst >= dstEnd {
				break decodeLoop
			}
			e = &lut[st[3]]
			out[dst] = e.symbol
			dst++
			bitposB -= int(e.bitsX)
			st[3] = (bitsB & e.x) + uint32(e.w)
			bitsB >>= e.bitsX
			if dst >= dstEnd {
				break decodeLoop
			}
			bitsB |= rdbe32(s, ptrB-4) << uint(bitposB)
			ptrB -= (31 - bitposB) >> 3
			bitposB |= 24
			e = &lut[st[4]]
			out[dst] = e.symbol
			dst++
			bitposB -= int(e.bitsX)
			st[4] = (bitsB & e.x) + uint32(e.w)
			bitsB >>= e.bitsX
			if dst >= dstEnd {
				break decodeLoop
			}
		}
	}

	if ptrB-ptrF+(bitposF>>3)+(bitposB>>3) != 0 {
		return false
	}

	statesOr := st[0] | st[1] | st[2] | st[3] | st[4]
	if statesOr&^0xFF != 0 {
		return false
	}

	out[dstEnd+0] = uint8(st[0])
	out[dstEnd+1] = uint8(st[1])
	out[dstEnd+2] = uint8(st[2])
	out[dstEnd+3] = uint8(st[3])
	out[dstEnd+4] = uint8(st[4])
	return true
}

func krakDecodeTans(s []byte, src, srcSize int, out []byte, dst, dstSize int, scr scratch) int {
	if srcSize < 8 || dstSize < 5 {
		return -1
	}

	srcEnd := src + srcSize

	var br bitReader
	var td tansData

	br.s = s
	br.bitpos = 24
	br.bits = 0
	br.p = src
	br.pEnd = srcEnd
	br.refill()

	// Reserved bit.
	if br.readBitNoRefill() != 0 {
		return -1
	}

	lBits := br.readBitsNoRefill(2) + 8

	if !tansDecodeTable(&br, lBits, &td) {
		return -1
	}

	src = br.p - (24-br.bitpos)/8

	if src >= srcEnd {
		return -1
	}

	lutSpaceRequired := ((8 << uint(lBits)) + 15) &^ 15
	if lutSpaceRequired > scr.avail() {
		return -1
	}

	var p tansDecoderParams
	p.out = out
	p.s = s
	p.dst = dst
	p.dstEnd = dst + dstSize - 5
	if n := 1 << uint(lBits); cap(scr.pool.tansLut) < n {
		scr.pool.tansLut = make([]tansLutEnt, n)
	} else {
		scr.pool.tansLut = scr.pool.tansLut[:n]
	}
	p.lut = scr.pool.tansLut
	tansInitLut(&td, lBits, p.lut)

	lMask := uint32(1<<uint(lBits)) - 1
	bitsF := rd32(s, src)
	src += 4
	bitsB := rdbe32(s, srcEnd-4)
	srcEnd -= 4
	bitposF, bitposB := 32, 32

	p.state[0] = bitsF & lMask
	p.state[1] = bitsB & lMask
	bitsF >>= uint(lBits)
	bitposF -= lBits
	bitsB >>= uint(lBits)
	bitposB -= lBits

	p.state[2] = bitsF & lMask
	p.state[3] = bitsB & lMask
	bitsF >>= uint(lBits)
	bitposF -= lBits
	bitsB >>= uint(lBits)
	bitposB -= lBits

	bitsF |= rd32(s, src) << uint(bitposF)
	src += (31 - bitposF) >> 3
	bitposF |= 24

	p.state[4] = bitsF & lMask
	bitsF >>= uint(lBits)
	bitposF -= lBits

	p.bitsF = bitsF
	p.ptrF = src - (bitposF >> 3)
	p.bitposF = bitposF & 7

	p.bitsB = bitsB
	p.ptrB = srcEnd + (bitposB >> 3)
	p.bitposB = bitposB & 7

	if !tansDecode(&p) {
		return -1
	}

	return srcSize
}
