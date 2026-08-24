package oodle

type leviathanLzTable struct {
	offsStream     []int32
	offsStreamSize int
	lenStream      []int32
	lenStreamSize  int
	litStream      [16]stream
	litStreamSize  [16]int
	litStreamTotal int
	multiCmdPtr    [8]stream
	cmdStream      stream
	hasCmdStream   bool
	cmdStreamSize  int
}

func leviathanReadLzTable(chunkType int, s []byte, src, srcEnd int,
	out []byte, dst, dstSize, offset int,
	scr scratch, lzt *leviathanLzTable) bool {

	if chunkType > 5 {
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

	offsScaling := 0
	var packedOffsExtra stream
	var packedOffs stream
	var n, decodeCount int

	offsStreamLimit := dstSize / 3

	if at(s, src)&0x80 == 0 {
		n, packedOffs, lzt.offsStreamSize = krakenDecodeBytes(s, src, srcEnd, scr.buf, scr.cur,
			minInt(scr.avail(), offsStreamLimit), false, scr)
		if n < 0 {
			return false
		}
		src += n
		scr.cur += lzt.offsStreamSize
	} else {
		offsScaling = int(at(s, src)) - 127
		src++

		n, packedOffs, lzt.offsStreamSize = krakenDecodeBytes(s, src, srcEnd, scr.buf, scr.cur,
			minInt(scr.avail(), offsStreamLimit), false, scr)
		if n < 0 {
			return false
		}
		src += n
		scr.cur += lzt.offsStreamSize

		if offsScaling != 1 {
			n, packedOffsExtra, decodeCount = krakenDecodeBytes(s, src, srcEnd, scr.buf, scr.cur,
				minInt(scr.avail(), offsStreamLimit), false, scr)
			if n < 0 || decodeCount != lzt.offsStreamSize {
				return false
			}
			src += n
			scr.cur += decodeCount
		}
	}

	// Packed literal length stream, bounded by a fifth of dstSize.
	var packedLen stream
	n, packedLen, lzt.lenStreamSize = krakenDecodeBytes(s, src, srcEnd, scr.buf, scr.cur,
		minInt(scr.avail(), dstSize/5), false, scr)
	if n < 0 {
		return false
	}
	src += n
	scr.cur += lzt.lenStreamSize

	scr.cur = alignUp(scr.cur, 16)
	scr.cur += lzt.offsStreamSize * 4
	scr.cur = alignUp(scr.cur, 16)
	scr.cur += lzt.lenStreamSize * 4

	if scr.cur > scr.end {
		return false
	}

	scr.pool.offs = growI32(scr.pool.offs, lzt.offsStreamSize)
	scr.pool.lens = growI32(scr.pool.lens, lzt.lenStreamSize)
	lzt.offsStream = scr.pool.offs
	lzt.lenStream = scr.pool.lens

	if chunkType <= 1 {
		var chunk stream
		n, chunk, decodeCount = krakenDecodeBytes(s, src, srcEnd, scr.buf, scr.cur,
			minInt(scr.avail(), dstSize), true, scr)
		if n < 0 {
			return false
		}
		src += n
		lzt.litStream[0] = chunk
		lzt.litStreamSize[0] = decodeCount
	} else {
		arrayCount := 16
		if chunkType == 2 {
			arrayCount = 2
		} else if chunkType == 3 {
			arrayCount = 4
		}
		n, decodeCount = krakenDecodeMultiArray(s, src, srcEnd, scr.buf, scr.cur, scr.end,
			lzt.litStream[:], lzt.litStreamSize[:], arrayCount, true, scr)
		if n < 0 {
			return false
		}
		src += n
	}
	scr.cur += decodeCount
	lzt.litStreamTotal = decodeCount

	if src >= srcEnd {
		return false
	}

	if at(s, src)&0x80 == 0 {
		var chunk stream
		n, chunk, decodeCount = krakenDecodeBytes(s, src, srcEnd, scr.buf, scr.cur,
			minInt(scr.avail(), dstSize), true, scr)
		if n < 0 {
			return false
		}
		src += n
		lzt.cmdStream = chunk
		lzt.hasCmdStream = true
		lzt.cmdStreamSize = decodeCount
		scr.cur += decodeCount
	} else {
		if at(s, src) != 0x83 {
			return false
		}
		src++
		// The stream lengths are not needed: the command count bounds the walk.
		var multiCmdLens [8]int
		n, decodeCount = krakenDecodeMultiArray(s, src, srcEnd, scr.buf, scr.cur, scr.end,
			lzt.multiCmdPtr[:], multiCmdLens[:], 8, true, scr)
		if n < 0 {
			return false
		}
		src += n
		lzt.hasCmdStream = false
		lzt.cmdStreamSize = decodeCount
		scr.cur += decodeCount
	}

	if dstSize > scr.end-scr.cur {
		return false
	}

	return krakenUnpackOffsets(s, src, srcEnd, packedOffs, packedOffsExtra,
		lzt.offsStreamSize, offsScaling,
		packedLen, lzt.lenStreamSize,
		lzt.offsStream, lzt.lenStream, false)
}

type levState struct {
	o            []byte
	dst          int
	lenStream    []int32
	li           int
	matchZoneEnd int
	lastOffset   int
}

func (st *levState) nextLen() int32 {
	if st.li < len(st.lenStream) {
		return st.lenStream[st.li]
	}
	return 0
}

type levMode interface {
	copyLiterals(cmd uint32, st *levState) bool
	copyFinalLiterals(finalLen int, st *levState)
	// overrun reports that the mode read past the end of a literal stream. The
	// C original cannot notice this and returns the bytes that happen to follow
	// in memory. It only occurs when the caller asked for more output than the
	// stream holds.
	overrun() bool
}

// levRaw copies literals unchanged.
type levRaw struct {
	lit  stream
	i    int
	size int
}

func (m *levRaw) overrun() bool { return m.i > m.size }

func (m *levRaw) copyLiterals(cmd uint32, st *levState) bool {
	litlen := int((cmd >> 3) & 3)
	if litlen == 3 {
		litlen = int(uint32(st.nextLen()) & 0xffffff)
		st.li++
	}
	o, dst := st.o, st.dst
	copy64(o, dst, m.lit.buf, m.lit.i+m.i)
	if litlen > 8 {
		copy64(o, dst+8, m.lit.buf, m.lit.i+m.i+8)
		if litlen > 16 {
			copy64(o, dst+16, m.lit.buf, m.lit.i+m.i+16)
			if litlen > 24 {
				if litlen > st.matchZoneEnd-dst {
					return false
				}
				for litlen > 24 {
					copy64(o, dst+24, m.lit.buf, m.lit.i+m.i+24)
					litlen -= 8
					dst += 8
					m.i += 8
				}
			}
		}
	}
	st.dst = dst + litlen
	m.i += litlen
	return true
}

func (m *levRaw) copyFinalLiterals(finalLen int, st *levState) {
	o, dst := st.o, st.dst
	for finalLen >= 64 {
		copy64Bytes(o, dst, m.lit.buf, m.lit.i+m.i)
		dst += 64
		m.i += 64
		finalLen -= 64
	}
	for finalLen >= 8 {
		copy64(o, dst, m.lit.buf, m.lit.i+m.i)
		dst += 8
		m.i += 8
		finalLen -= 8
	}
	oc := o
	for ; finalLen > 0; finalLen-- {
		oc[dst] = m.lit.byteAt(m.i)
		m.i++
		dst++
	}
	st.dst = dst
}

// levSub adds each literal to the byte one match distance back.
type levSub struct {
	lit  stream
	i    int
	size int
}

func (m *levSub) overrun() bool { return m.i > m.size }

func (m *levSub) copyLiterals(cmd uint32, st *levState) bool {
	litlen := int((cmd >> 3) & 3)
	if litlen == 3 {
		litlen = int(uint32(st.nextLen()) & 0xffffff)
		st.li++
	}
	o, dst, lo := st.o, st.dst, st.lastOffset
	copy64Add(o, dst, m.lit.buf, m.lit.i+m.i, dst+lo)
	if litlen > 8 {
		copy64Add(o, dst+8, m.lit.buf, m.lit.i+m.i+8, dst+lo+8)
		if litlen > 16 {
			copy64Add(o, dst+16, m.lit.buf, m.lit.i+m.i+16, dst+lo+16)
			if litlen > 24 {
				if litlen > st.matchZoneEnd-dst {
					return false
				}
				for litlen > 24 {
					copy64Add(o, dst+24, m.lit.buf, m.lit.i+m.i+24, dst+lo+24)
					litlen -= 8
					dst += 8
					m.i += 8
				}
			}
		}
	}
	st.dst = dst + litlen
	m.i += litlen
	return true
}

func (m *levSub) copyFinalLiterals(finalLen int, st *levState) {
	o, dst, lo := st.o, st.dst, st.lastOffset
	for finalLen >= 8 {
		copy64Add(o, dst, m.lit.buf, m.lit.i+m.i, dst+lo)
		dst += 8
		m.i += 8
		finalLen -= 8
	}
	oc := o
	for ; finalLen > 0; finalLen-- {
		oc[dst] = m.lit.byteAt(m.i) + oc[dst+lo]
		m.i++
		dst++
	}
	st.dst = dst
}

// levLamSub takes the first literal of each run from a separate stream.
type levLamSub struct {
	lit     stream
	i       int
	size    int
	lam     stream
	lamI    int
	lamSize int
}

func (m *levLamSub) overrun() bool { return m.i > m.size || m.lamI > m.lamSize }

func (m *levLamSub) copyLiterals(cmd uint32, st *levState) bool {
	litCmd := cmd & 0x18
	if litCmd == 0 {
		return true
	}

	litlen := int(litCmd >> 3)
	if litlen == 3 {
		litlen = int(uint32(st.nextLen()) & 0xffffff)
		st.li++
	}

	if litlen == 0 {
		return false // lamsub needs one literal
	}
	litlen--

	o, dst, lo := st.o, st.dst, st.lastOffset
	oc := o
	oc[dst] = m.lam.byteAt(m.lamI) + oc[dst+lo]
	m.lamI++
	dst++

	copy64Add(o, dst, m.lit.buf, m.lit.i+m.i, dst+lo)
	if litlen > 8 {
		copy64Add(o, dst+8, m.lit.buf, m.lit.i+m.i+8, dst+lo+8)
		if litlen > 16 {
			copy64Add(o, dst+16, m.lit.buf, m.lit.i+m.i+16, dst+lo+16)
			if litlen > 24 {
				if litlen > st.matchZoneEnd-dst {
					return false
				}
				for litlen > 24 {
					copy64Add(o, dst+24, m.lit.buf, m.lit.i+m.i+24, dst+lo+24)
					litlen -= 8
					dst += 8
					m.i += 8
				}
			}
		}
	}
	st.dst = dst + litlen
	m.i += litlen
	return true
}

func (m *levLamSub) copyFinalLiterals(finalLen int, st *levState) {
	o, dst, lo := st.o, st.dst, st.lastOffset
	oc := o
	oc[dst] = m.lam.byteAt(m.lamI) + oc[dst+lo]
	m.lamI++
	dst++
	finalLen--

	for finalLen >= 8 {
		copy64Add(o, dst, m.lit.buf, m.lit.i+m.i, dst+lo)
		dst += 8
		m.i += 8
		finalLen -= 8
	}
	for ; finalLen > 0; finalLen-- {
		oc[dst] = m.lit.byteAt(m.i) + oc[dst+lo]
		m.i++
		dst++
	}
	st.dst = dst
}

// levSubAnd interleaves NUM literal streams by output position.
type levSubAnd struct {
	num     int
	mask    int
	lit     [16]stream
	i       [16]int
	size    [16]int
	dstBase int
}

func (m *levSubAnd) overrun() bool {
	for k := 0; k < m.num; k++ {
		if m.i[k] > m.size[k] {
			return true
		}
	}
	return false
}

func (m *levSubAnd) slot(dst int) int { return (dst - m.dstBase) & m.mask }

func (m *levSubAnd) one(st *levState, dst int) {
	oc := st.o
	k := m.slot(dst)
	oc[dst] = m.lit[k].byteAt(m.i[k]) + oc[dst+st.lastOffset]
	m.i[k]++
}

func (m *levSubAnd) copyLiterals(cmd uint32, st *levState) bool {
	litCmd := cmd & 0x18

	if litCmd == 0x18 {
		litlen := int(uint32(st.nextLen()) & 0xffffff)
		st.li++
		if litlen > st.matchZoneEnd-st.dst {
			return false
		}
		for ; litlen > 0; litlen-- {
			m.one(st, st.dst)
			st.dst++
		}
	} else if litCmd != 0 {
		m.one(st, st.dst)
		st.dst++
		if litCmd == 0x10 {
			m.one(st, st.dst)
			st.dst++
		}
	}
	return true
}

func (m *levSubAnd) copyFinalLiterals(finalLen int, st *levState) {
	for ; finalLen > 0; finalLen-- {
		m.one(st, st.dst)
		st.dst++
	}
}

// levO1 picks the literal stream from the high nibble of the previous byte.
type levO1 struct {
	lit     [16]stream
	i       [16]int
	size    [16]int
	nextLit [16]byte
}

// levO1 keeps one byte of each stream in nextLit, so its index legitimately
// runs one past the stream size.
func (m *levO1) overrun() bool {
	for k := 0; k < 16; k++ {
		if m.i[k] > m.size[k]+1 {
			return true
		}
	}
	return false
}

func (m *levO1) take(slot int) byte {
	v := m.nextLit[slot]
	m.nextLit[slot] = m.lit[slot].byteAt(m.i[slot])
	m.i[slot]++
	return v
}

func (m *levO1) copyLiterals(cmd uint32, st *levState) bool {
	litCmd := cmd & 0x18
	oc := st.o

	if litCmd == 0x18 {
		litlen := int(st.nextLen())
		st.li++
		if int32(litlen) <= 0 {
			return false
		}
		context := int(oc[st.dst-1])
		for ; litlen > 0; litlen-- {
			slot := context >> 4
			v := m.take(slot)
			oc[st.dst] = v
			st.dst++
			context = int(v)
		}
	} else if litCmd != 0 {
		context := int(oc[st.dst-1])
		slot := context >> 4
		v := m.take(slot)
		oc[st.dst] = v
		st.dst++
		context = int(v)
		if litCmd == 0x10 {
			slot = context >> 4
			v = m.take(slot)
			oc[st.dst] = v
			st.dst++
		}
	}
	return true
}

func (m *levO1) copyFinalLiterals(finalLen int, st *levState) {
	oc := st.o
	context := int(oc[st.dst-1])
	for ; finalLen > 0; finalLen-- {
		slot := context >> 4
		v := m.take(slot)
		oc[st.dst] = v
		st.dst++
		context = int(v)
	}
}

func newLevMode(chunkType int, lzt *leviathanLzTable, dstStart int) levMode {
	switch chunkType {
	case 0:
		return &levSub{lit: lzt.litStream[0], size: lzt.litStreamSize[0]}
	case 1:
		return &levRaw{lit: lzt.litStream[0], size: lzt.litStreamSize[0]}
	case 2:
		return &levLamSub{
			lit: lzt.litStream[0], size: lzt.litStreamSize[0],
			lam: lzt.litStream[1], lamSize: lzt.litStreamSize[1],
		}
	case 3:
		m := &levSubAnd{num: 4, mask: 3, dstBase: dstStart}
		for i := 0; i < 4; i++ {
			m.lit[i] = lzt.litStream[i]
			m.size[i] = lzt.litStreamSize[i]
		}
		return m
	case 4:
		m := &levO1{}
		for i := 0; i < 16; i++ {
			m.lit[i] = lzt.litStream[i]
			m.size[i] = lzt.litStreamSize[i]
			m.nextLit[i] = m.lit[i].byteAt(0)
			m.i[i] = 1
		}
		return m
	case 5:
		m := &levSubAnd{num: 16, mask: 15, dstBase: dstStart}
		for i := 0; i < 16; i++ {
			m.lit[i] = lzt.litStream[i]
			m.size[i] = lzt.litStreamSize[i]
		}
		return m
	}
	return nil
}

// leviathanProcessLz reports whether it succeeded, and whether it stopped
// because the stream ends before dstEnd.
func leviathanProcessLz(chunkType int, lzt *leviathanLzTable, o []byte,
	dst, dstStart, dstEnd, windowBase int) (ok, short bool) {

	multiCmd := !lzt.hasCmdStream

	cmdStream := lzt.cmdStream
	ci := 0
	cEnd := lzt.cmdStreamSize
	lenStream := lzt.lenStream
	li := 0
	lenStreamEnd := len(lenStream)

	offsStream := lzt.offsStream
	oi := 0

	matchZoneEnd := dstStart
	if dstEnd-dstStart >= 16 {
		matchZoneEnd = dstEnd - 16
	}

	var recentOffs [16]int32
	recentOffs[8] = -8
	recentOffs[9] = -8
	recentOffs[10] = -8
	recentOffs[11] = -8
	recentOffs[12] = -8
	recentOffs[13] = -8
	recentOffs[14] = -8

	offset := -8

	mode := newLevMode(chunkType, lzt, dstStart)
	if mode == nil {
		return false, false
	}

	st := &levState{o: o, dst: dst, lenStream: lenStream, li: li, matchZoneEnd: matchZoneEnd, lastOffset: offset}

	// Multi command mode keeps eight command streams, picked by output position.
	var mcs [8]stream
	cmdStreamLeft := 0
	if multiCmd {
		for i := 0; i < 8; i++ {
			mcs[i] = lzt.multiCmdPtr[i]
		}
		cmdStreamLeft = lzt.cmdStreamSize
	}

	for {
		var cmd uint32
		var slot int

		if !multiCmd {
			if ci >= cEnd {
				break
			}
			cmd = uint32(cmdStream.byteAt(ci))
			ci++
		} else {
			if cmdStreamLeft == 0 {
				break
			}
			cmdStreamLeft--
			slot = (st.dst - dstStart) & 7
			cmd = uint32(mcs[slot].byteAt(0))
			mcs[slot].i++
		}

		offsIndex := int(cmd >> 5)
		matchlen := uint32(cmd&7) + 2

		if oi < len(offsStream) {
			recentOffs[15] = offsStream[oi]
		} else {
			recentOffs[15] = 0
		}

		st.lastOffset = offset
		if !mode.copyLiterals(cmd, st) {
			return false, false
		}

		offset = int(recentOffs[offsIndex+8])

		// Permute the recent offsets table: the four entries at offsIndex
		// move up one slot, and the four above them move up four.
		a0, a1, a2, a3 := recentOffs[offsIndex], recentOffs[offsIndex+1], recentOffs[offsIndex+2], recentOffs[offsIndex+3]
		b0, b1, b2, b3 := recentOffs[offsIndex+4], recentOffs[offsIndex+5], recentOffs[offsIndex+6], recentOffs[offsIndex+7]
		recentOffs[offsIndex+1], recentOffs[offsIndex+2] = a0, a1
		recentOffs[offsIndex+3], recentOffs[offsIndex+4] = a2, a3
		recentOffs[offsIndex+5], recentOffs[offsIndex+6] = b0, b1
		recentOffs[offsIndex+7], recentOffs[offsIndex+8] = b2, b3
		recentOffs[8] = int32(offset)
		if offsIndex == 7 {
			oi++
		}

		if offset < windowBase-st.dst {
			return false, false
		}
		copyfrom := st.dst + offset

		if matchlen == 9 {
			if st.li >= lenStreamEnd {
				return false, false
			}
			lenStreamEnd--
			matchlen = uint32(lenStream[lenStreamEnd]) + 6
			dstCur := st.dst
			copy64(o, dstCur, o, copyfrom)
			copy64(o, dstCur+8, o, copyfrom+8)
			nextDst := dstCur + int(matchlen)
			if matchlen > 16 {
				if uint64(matchlen) > uint64(int64(dstEnd-8-dstCur)) {
					return false, false
				}
				copy64(o, dstCur+16, o, copyfrom+16)
				for {
					copy64(o, dstCur+24, o, copyfrom+24)
					matchlen -= 8
					dstCur += 8
					copyfrom += 8
					if matchlen <= 24 {
						break
					}
				}
			}
			st.dst = nextDst
		} else {
			copy64(o, st.dst, o, copyfrom)
			st.dst += int(matchlen)
		}
	}

	if oi != len(offsStream) || st.li != lenStreamEnd {
		return false, false
	}

	st.lastOffset = offset
	if st.dst < dstEnd {
		mode.copyFinalLiterals(dstEnd-st.dst, st)
	} else if st.dst != dstEnd {
		return false, false
	}
	if mode.overrun() {
		return false, true
	}
	return true, false
}

func leviathanProcessLzRuns(chunkType int, o []byte, dst, dstSize, offset int, lzt *leviathanLzTable) (ok, short bool) {
	dstCur := dst
	if offset == 0 {
		dstCur += 8
	}
	dstEnd := dst + dstSize
	dstStart := dst - offset
	if chunkType > 5 {
		return false, false
	}
	return leviathanProcessLz(chunkType, lzt, o, dstCur, dst, dstEnd, dstStart)
}

func leviathanDecodeQuantum(o []byte, dst, dstEnd, dstStart int,
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
				if scratchUsage < sizeofLeviathanLzTable {
					return -1
				}
				inner := scr.window(scr.cur+sizeofLeviathanLzTable, scr.cur+scratchUsage)
				var lzt leviathanLzTable
				if !leviathanReadLzTable(mode, s, src, src+srcUsed, o, dst, dstCount, dst-dstStart, inner, &lzt) {
					return -1
				}
				if ok, short := leviathanProcessLzRuns(mode, o, dst, dstCount, dst-dstStart, &lzt); !ok {
					if short {
						return resultShortStream
					}
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
