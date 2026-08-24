package oodle

// addBytes64 adds two 64 bit words byte by byte, with no carry between bytes.
func addBytes64(a, b uint64) uint64 {
	const h = uint64(0x8080808080808080)
	return ((a &^ h) + (b &^ h)) ^ ((a ^ b) & h)
}

// copy64 is COPY_64: an 8 byte load followed by an 8 byte store.
func copy64(dst []byte, d int, src []byte, s int) {
	wr64(dst, d, rd64(src, s))
}

// copy64Add is COPY_64_ADD: 8 bytes of src plus 8 bytes of dst at t.
func copy64Add(dst []byte, d int, src []byte, s int, t int) {
	wr64(dst, d, addBytes64(rd64(src, s), rd64(dst, t)))
}

// copy64Bytes is COPY_64_BYTES: 64 bytes in four 16 byte steps.
func copy64Bytes(dst []byte, d int, src []byte, s int) {
	for k := 0; k < 64; k += 16 {
		lo := rd64(src, s+k)
		hi := rd64(src, s+k+8)
		wr64(dst, d+k, lo)
		wr64(dst, d+k+8, hi)
	}
}
