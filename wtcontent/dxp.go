package wtcontent

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// dxpNameBase is where the name blob starts inside the pack block. The name
// index entries are absolute in block terms, so they carry this offset too.
const dxpNameBase = 0x38

// dxpRecordSize is the size of one texture record, and dxpRecordOffsetAt is
// where the file offset of the payload sits inside it. No other field is used
// here.
const (
	dxpRecordSize     = 0x18
	dxpRecordOffsetAt = 0xC
)

// ErrDXPNotFound reports a texture name that is not in the pack.
var ErrDXPNotFound = errors.New("dxp: no such texture")

// DXP is a DDSx texture pack, the container War Thunder ships as
// "<name>.dxp.bin" under content/*/res.
type DXP struct {
	raw     []byte
	names   []string
	headers []DDSxHeader
	offsets []uint32
}

// ReadDXP parses the index of a DDSx texture pack. Payloads stay in raw and
// are decompressed on demand by Texture.
func ReadDXP(raw []byte) (*DXP, error) {
	if len(raw) < 0x10 {
		return nil, errors.New("dxp: file too short")
	}
	count := int(binary.LittleEndian.Uint32(raw[8:12]))
	blockSize := int(binary.LittleEndian.Uint32(raw[12:16]))
	block, err := sliceAt(raw, 0x10, blockSize)
	if err != nil {
		return nil, fmt.Errorf("dxp: index block: %w", err)
	}
	if blockSize < dxpNameBase {
		return nil, errors.New("dxp: index block too short")
	}
	if count < 0 {
		return nil, errors.New("dxp: negative texture count")
	}

	nameIdxAt := int(binary.LittleEndian.Uint32(block[0:4]))
	nameIdxCount := int(binary.LittleEndian.Uint32(block[4:8]))
	headersAt := int(binary.LittleEndian.Uint32(block[16:20]))
	headersCount := int(binary.LittleEndian.Uint32(block[20:24]))
	recordsAt := int(binary.LittleEndian.Uint32(block[32:36]))
	recordsCount := int(binary.LittleEndian.Uint32(block[36:40]))

	if headersCount != count || recordsCount != count || nameIdxCount != count {
		return nil, fmt.Errorf("dxp: index counts disagree: files %d, names %d, headers %d, records %d",
			count, nameIdxCount, headersCount, recordsCount)
	}

	names, err := dxpNames(block, nameIdxAt, count)
	if err != nil {
		return nil, err
	}

	d := &DXP{
		raw:     raw,
		names:   names,
		headers: make([]DDSxHeader, count),
		offsets: make([]uint32, count),
	}
	for i := range count {
		hb, err := sliceAt(block, headersAt+i*DDSxHeaderSize, DDSxHeaderSize)
		if err != nil {
			return nil, fmt.Errorf("dxp: header %d: %w", i, err)
		}
		d.headers[i], err = ParseDDSxHeader(hb)
		if err != nil {
			return nil, fmt.Errorf("dxp: header %d: %w", i, err)
		}
		rb, err := sliceAt(block, recordsAt+i*dxpRecordSize, dxpRecordSize)
		if err != nil {
			return nil, fmt.Errorf("dxp: record %d: %w", i, err)
		}
		d.offsets[i] = binary.LittleEndian.Uint32(rb[dxpRecordOffsetAt : dxpRecordOffsetAt+4])
	}
	return d, nil
}

// dxpNames reads the name blob that sits between dxpNameBase and the index
// table. The table holds one little endian uint64 per name, each pointing at
// the start of a null terminated name.
func dxpNames(block []byte, idxAt, count int) ([]string, error) {
	if idxAt < dxpNameBase {
		return nil, errors.New("dxp: name index starts before the name blob")
	}
	idx, err := sliceAt(block, idxAt, count*8)
	if err != nil {
		return nil, fmt.Errorf("dxp: name index: %w", err)
	}
	names := make([]string, count)
	for i := range count {
		at := int(binary.LittleEndian.Uint64(idx[i*8 : i*8+8]))
		if at < dxpNameBase || at >= idxAt {
			return nil, fmt.Errorf("dxp: name %d is out of the name blob", i)
		}
		end := bytes.IndexByte(block[at:idxAt], 0)
		if end < 0 {
			return nil, fmt.Errorf("dxp: name %d is not terminated", i)
		}
		names[i] = string(block[at : at+end])
	}
	return names, nil
}

// Names lists the textures in the pack, in index order.
func (d *DXP) Names() []string {
	return append([]string(nil), d.names...)
}

// Header returns the DDSx header of one texture.
func (d *DXP) Header(i int) DDSxHeader {
	return d.headers[i]
}

// Index finds a texture by name. Pack names can carry a "*" or "$" suffix, so
// an exact match is tried first and a prefix match second.
func (d *DXP) Index(name string) int {
	for i, n := range d.names {
		if n == name {
			return i
		}
	}
	for i, n := range d.names {
		base, _, _ := strings.Cut(n, "*")
		base, _, _ = strings.Cut(base, "$")
		if base == name {
			return i
		}
	}
	return -1
}

// Payload returns the decompressed mip data of one texture.
func (d *DXP) Payload(i int) ([]byte, error) {
	if i < 0 || i >= len(d.headers) {
		return nil, fmt.Errorf("dxp: index %d is out of %d textures", i, len(d.headers))
	}
	h := d.headers[i]
	if h.PackedSz == 0 {
		return nil, fmt.Errorf("dxp: texture %q is empty", d.names[i])
	}
	payload, err := sliceAt(d.raw, int(d.offsets[i]), int(h.PackedSz))
	if err != nil {
		return nil, fmt.Errorf("dxp: texture %q payload: %w", d.names[i], err)
	}
	return h.Decode(payload)
}

// Texture returns one texture as a DDS file.
func (d *DXP) Texture(name string) ([]byte, error) {
	i := d.Index(name)
	if i < 0 {
		return nil, fmt.Errorf("%w: %q", ErrDXPNotFound, name)
	}
	data, err := d.Payload(i)
	if err != nil {
		return nil, err
	}
	return d.headers[i].DDS(data)
}
