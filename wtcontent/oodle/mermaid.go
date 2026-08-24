package oodle

// mermaidLzTable also serves Selkie: the two share one on-disk format.
type mermaidLzTable struct {
	cmdStream    stream
	cmdStreamEnd int

	lengthStream int // index into the source

	litStream    stream
	litStreamEnd int

	off16Stream    stream // 16 bit elements
	off16StreamEnd int    // element count end, in elements from off16Stream

	off32Stream    []uint32
	off32StreamEnd int

	off32Stream1 []uint32
	off32Stream2 []uint32
	off32Size1   int
	off32Size2   int

	cmdStream2Offs    int
	cmdStream2OffsEnd int
}

func mermaidDecodeFarOffsets(s []byte, src, srcEnd int, output []uint32, offset int64) int {
	srcCur := src

	if offset < 0xC00000-1 {
		for i := range output {
			if srcEnd-srcCur < 3 {
				return -1
			}
			off := uint32(at(s, srcCur)) | uint32(at(s, srcCur+1))<<8 | uint32(at(s, srcCur+2))<<16
			srcCur += 3
			output[i] = off
			if int64(off) > offset {
				return -1
			}
		}
		return srcCur - src
	}

	for i := range output {
		if srcEnd-srcCur < 3 {
			return -1
		}
		off := uint32(at(s, srcCur)) | uint32(at(s, srcCur+1))<<8 | uint32(at(s, srcCur+2))<<16
		srcCur += 3

		if off >= 0xc00000 {
			if srcCur == srcEnd {
				return -1
			}
			off += uint32(at(s, srcCur)) << 22
			srcCur++
		}
		output[i] = off
		if int64(off) > offset {
			return -1
		}
	}
	return srcCur - src
}

func mermaidReadLzTable(mode int, s []byte, src, srcEnd int,
	out []byte, dst, dstSize int, offset int64,
	scr scratch, lz *mermaidLzTable) bool {

	if mode > 1 {
		return false
	}
	if srcEnd-src < 10 {
		return false
	}

	if offset == 0 {
		copy64(out, dst, s, src)
		dst += 8
		src += 8
	}

	// Literal stream.
	n, chunk, decodeCount := krakenDecodeBytes(s, src, srcEnd, scr.buf, scr.cur,
		minInt(scr.avail(), dstSize), false, scr)
	if n < 0 {
		return false
	}
	src += n
	lz.litStream = chunk
	lz.litStreamEnd = decodeCount
	scr.cur += decodeCount

	// Flag stream.
	n, chunk, decodeCount = krakenDecodeBytes(s, src, srcEnd, scr.buf, scr.cur,
		minInt(scr.avail(), dstSize), false, scr)
	if n < 0 {
		return false
	}
	src += n
	lz.cmdStream = chunk
	lz.cmdStreamEnd = decodeCount
	scr.cur += decodeCount

	lz.cmdStream2OffsEnd = decodeCount
	if dstSize <= 0x10000 {
		lz.cmdStream2Offs = decodeCount
	} else {
		if srcEnd-src < 2 {
			return false
		}
		lz.cmdStream2Offs = int(rd16(s, src))
		src += 2
		if lz.cmdStream2Offs > lz.cmdStream2OffsEnd {
			return false
		}
	}

	if srcEnd-src < 2 {
		return false
	}

	off16Count := int(rd16(s, src))
	if off16Count == 0xffff {
		src += 2
		var off16Hi, off16Lo stream
		var hiCount, loCount int
		n, off16Hi, hiCount = krakenDecodeBytes(s, src, srcEnd, scr.buf, scr.cur,
			minInt(scr.avail(), dstSize>>1), false, scr)
		if n < 0 {
			return false
		}
		src += n
		scr.cur += hiCount

		n, off16Lo, loCount = krakenDecodeBytes(s, src, srcEnd, scr.buf, scr.cur,
			minInt(scr.avail(), dstSize>>1), false, scr)
		if n < 0 {
			return false
		}
		src += n
		scr.cur += loCount

		if loCount != hiCount {
			return false
		}
		scr.cur = alignUp(scr.cur, 2)
		if scr.cur+loCount*2 > scr.end {
			return false
		}
		base := scr.cur
		sc := scr.buf
		for i := 0; i < loCount; i++ {
			v := uint16(off16Lo.byteAt(i)) + uint16(off16Hi.byteAt(i))*256
			sc[base+2*i] = byte(v)
			sc[base+2*i+1] = byte(v >> 8)
		}
		lz.off16Stream = stream{scr.buf, base}
		lz.off16StreamEnd = loCount
		scr.cur += loCount * 2
	} else {
		lz.off16Stream = stream{s, src + 2}
		lz.off16StreamEnd = off16Count
		src += 2 + off16Count*2
	}

	if srcEnd-src < 3 {
		return false
	}
	tmp := uint32(at(s, src)) | uint32(at(s, src+1))<<8 | uint32(at(s, src+2))<<16
	src += 3

	if tmp != 0 {
		off32Size1 := int(tmp >> 12)
		off32Size2 := int(tmp & 0xFFF)
		if off32Size1 == 4095 {
			if srcEnd-src < 2 {
				return false
			}
			off32Size1 = int(rd16(s, src))
			src += 2
		}
		if off32Size2 == 4095 {
			if srcEnd-src < 2 {
				return false
			}
			off32Size2 = int(rd16(s, src))
			src += 2
		}
		lz.off32Size1 = off32Size1
		lz.off32Size2 = off32Size2

		if scr.cur+4*(off32Size2+off32Size1)+64 > scr.end {
			return false
		}
		scr.cur = alignUp(scr.cur, 4)
		scr.cur += off32Size1*4 + 32
		scr.cur += off32Size2*4 + 32

		scr.pool.far1 = growU32(scr.pool.far1, off32Size1)
		scr.pool.far2 = growU32(scr.pool.far2, off32Size2)
		lz.off32Stream1 = scr.pool.far1
		lz.off32Stream2 = scr.pool.far2

		n = mermaidDecodeFarOffsets(s, src, srcEnd, lz.off32Stream1, offset)
		if n < 0 {
			return false
		}
		src += n

		n = mermaidDecodeFarOffsets(s, src, srcEnd, lz.off32Stream2, offset+0x10000)
		if n < 0 {
			return false
		}
		src += n
	} else {
		if scr.end-scr.cur < 32 {
			return false
		}
		lz.off32Size1 = 0
		lz.off32Size2 = 0
		lz.off32Stream1 = nil
		lz.off32Stream2 = nil
	}
	lz.lengthStream = src
	return true
}

// mermaidReadLength reads one length byte, with a two byte escape.
func mermaidReadLength(s []byte, ls *int, srcEnd int) (int, bool) {
	if srcEnd-*ls == 0 {
		return 0, false
	}
	length := int(at(s, *ls))
	if length > 251 {
		if srcEnd-*ls < 3 {
			return 0, false
		}
		length += int(rd16(s, *ls+1)) * 4
		*ls += 2
	}
	*ls++
	return length, true
}

// mermaidMode runs one 64k half. sub selects the literal transform: mode 0 adds
// the byte one match distance back, mode 1 copies literals unchanged.
func mermaidMode(sub bool, o []byte, dst, dstSize, dstStart int,
	s []byte, srcEnd int, lz *mermaidLzTable, savedDist *int32, startoff int) int {

	dstEnd := dst + dstSize
	ci := 0
	cEnd := lz.cmdStreamEnd
	lengthStream := lz.lengthStream
	lii := 0
	litEnd := lz.litStreamEnd
	off16i := 0
	off16End := lz.off16StreamEnd
	off32 := lz.off32Stream
	off32i := 0
	off32End := lz.off32StreamEnd
	recentOffs := int(*savedDist)
	dstBegin := dst

	oc := o
	dst += startoff

	for ci < cEnd {
		cmd := int(lz.cmdStream.byteAt(ci))
		ci++
		if cmd >= 24 {
			newDist := int(rd16(lz.off16Stream.buf, lz.off16Stream.i+2*off16i))
			litlen := cmd & 7
			if sub {
				copy64Add(o, dst, lz.litStream.buf, lz.litStream.i+lii, dst+recentOffs)
			} else {
				copy64(o, dst, lz.litStream.buf, lz.litStream.i+lii)
			}
			dst += litlen
			lii += litlen
			if cmd&0x80 == 0 {
				recentOffs = -newDist
				off16i++
			}
			match := dst + recentOffs
			copy64(o, dst, o, match)
			copy64(o, dst+8, o, match+8)
			dst += (cmd >> 3) & 0xF
		} else if cmd > 2 {
			length := cmd + 5

			if off32i == off32End || off32i >= len(off32) {
				return -1
			}
			match := dstBegin - int(off32[off32i])
			off32i++
			recentOffs = match - dst

			if dstEnd-dst < length {
				return -1
			}
			copy64(o, dst, o, match)
			copy64(o, dst+8, o, match+8)
			copy64(o, dst+16, o, match+16)
			copy64(o, dst+24, o, match+24)
			dst += length
		} else if cmd == 0 {
			length, ok := mermaidReadLength(s, &lengthStream, srcEnd)
			if !ok {
				return -1
			}
			length += 64
			if dstEnd-dst < length || litEnd-lii < length {
				return -1
			}
			for length > 0 {
				if sub {
					copy64Add(o, dst, lz.litStream.buf, lz.litStream.i+lii, dst+recentOffs)
					copy64Add(o, dst+8, lz.litStream.buf, lz.litStream.i+lii+8, dst+recentOffs+8)
				} else {
					copy64(o, dst, lz.litStream.buf, lz.litStream.i+lii)
					copy64(o, dst+8, lz.litStream.buf, lz.litStream.i+lii+8)
				}
				dst += 16
				lii += 16
				length -= 16
			}
			dst += length
			lii += length
		} else if cmd == 1 {
			length, ok := mermaidReadLength(s, &lengthStream, srcEnd)
			if !ok {
				return -1
			}
			length += 91

			if off16i == off16End {
				return -1
			}
			match := dst - int(rd16(lz.off16Stream.buf, lz.off16Stream.i+2*off16i))
			off16i++
			recentOffs = match - dst
			for length > 0 {
				copy64(o, dst, o, match)
				copy64(o, dst+8, o, match+8)
				dst += 16
				match += 16
				length -= 16
			}
			dst += length
		} else {
			length, ok := mermaidReadLength(s, &lengthStream, srcEnd)
			if !ok {
				return -1
			}
			length += 29
			if off32i == off32End || off32i >= len(off32) {
				return -1
			}
			match := dstBegin - int(off32[off32i])
			off32i++
			recentOffs = match - dst
			for length > 0 {
				copy64(o, dst, o, match)
				copy64(o, dst+8, o, match+8)
				dst += 16
				match += 16
				length -= 16
			}
			dst += length
		}
	}

	length := dstEnd - dst
	for length >= 8 {
		if sub {
			copy64Add(o, dst, lz.litStream.buf, lz.litStream.i+lii, dst+recentOffs)
		} else {
			copy64(o, dst, lz.litStream.buf, lz.litStream.i+lii)
		}
		dst += 8
		lii += 8
		length -= 8
	}
	for ; length > 0; length-- {
		if sub {
			oc[dst] = lz.litStream.byteAt(lii) + oc[dst+recentOffs]
		} else {
			oc[dst] = lz.litStream.byteAt(lii)
		}
		lii++
		dst++
	}

	if lii > litEnd {
		// Read past the end of the literal stream. This only occurs when the
		// caller asked for more output than the stream holds.
		return resultShortStream
	}

	*savedDist = int32(recentOffs)
	lz.lengthStream = lengthStream
	lz.off16Stream.i += 2 * off16i
	lz.off16StreamEnd -= off16i
	lz.litStream.i += lii
	lz.litStreamEnd -= lii
	return lengthStream
}

// mermaidProcessLzRuns reports whether it succeeded, and whether it stopped
// because the stream ends before the requested size.
func mermaidProcessLzRuns(mode int, s []byte, src, srcEnd int,
	o []byte, dst, dstSize, offset int, lz *mermaidLzTable) (ok, short bool) {

	dstStart := dst - offset
	savedDist := int32(-8)
	srcCur := -1
	cmdStreamBase := lz.cmdStream.i

	for iteration := 0; iteration != 2; iteration++ {
		dstSizeCur := dstSize
		if dstSizeCur > 0x10000 {
			dstSizeCur = 0x10000
		}

		if iteration == 0 {
			lz.off32Stream = lz.off32Stream1
			lz.off32StreamEnd = lz.off32Size1 * 4
			lz.cmdStream.i = cmdStreamBase
			lz.cmdStreamEnd = lz.cmdStream2Offs
		} else {
			lz.off32Stream = lz.off32Stream2
			lz.off32StreamEnd = lz.off32Size2 * 4
			lz.cmdStream.i = cmdStreamBase + lz.cmdStream2Offs
			lz.cmdStreamEnd = lz.cmdStream2OffsEnd - lz.cmdStream2Offs
		}

		startoff := 0
		if offset == 0 && iteration == 0 {
			startoff = 8
		}
		srcCur = mermaidMode(mode == 0, o, dst, dstSizeCur, dstStart, s, srcEnd, lz, &savedDist, startoff)
		if srcCur == resultShortStream {
			return false, true
		}
		if srcCur < 0 {
			return false, false
		}

		dst += dstSizeCur
		dstSize -= dstSizeCur
		if dstSize == 0 {
			break
		}
	}

	return srcCur == srcEnd, false
}

func mermaidDecodeQuantum(o []byte, dst, dstEnd, dstStart int,
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
				tempUsage := 2*dstCount + 32
				if tempUsage > 0x40000 {
					tempUsage = 0x40000
				}
				if tempUsage < sizeofMermaidLzTable {
					return -1
				}
				inner := scr.window(scr.cur+sizeofMermaidLzTable, scr.cur+tempUsage)
				var lz mermaidLzTable
				if !mermaidReadLzTable(mode, s, src, src+srcUsed, o, dst, dstCount,
					int64(dst-dstStart), inner, &lz) {
					return -1
				}
				if ok, short := mermaidProcessLzRuns(mode, s, src, src+srcUsed, o, dst, dstCount, dst-dstStart, &lz); !ok {
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
