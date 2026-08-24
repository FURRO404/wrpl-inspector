package wtcontent

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"

	"github.com/klauspost/compress/zstd"
	"github.com/maxsupermanhd/wrpl-inspector/v3/wtcontent/oodle"
)

// DDSxHeaderSize is the size of the header in front of every DDSx texture.
const DDSxHeaderSize = 0x20

// Compression methods, as stored in DDSxHeader.CMethod.
const (
	ddsxStored = 0x00
	ddsxZSTD   = 0x20
	ddsxLZMA   = 0x40
	ddsxOodle  = 0x60
	ddsxZlib   = 0x80
)

// ddsxFlagRevMipOrder marks a texture whose mip levels are stored smallest
// first. DDS wants them largest first.
const ddsxFlagRevMipOrder = 0x40000

// ErrDDSxFormat reports a DDSx pixel format this package cannot wrap in a DDS
// container. The payload still decompresses; only DDS needs the format.
var ErrDDSxFormat = errors.New("ddsx: pixel format is not a block compressed FourCC")

// DDSxHeader is the 32 byte header in front of every DDSx texture.
type DDSxHeader struct {
	Label    [4]byte
	D3DFmt   [4]byte // "DXT1", "DXT5", "BC7 " or "ATI1"
	Flags    uint32  // 24 bits
	CMethod  byte
	W, H     uint16
	Levels   byte
	HQLevels byte
	Depth    uint16
	BPP      uint16
	LQMip    byte
	MQMip    byte
	DxtShift byte
	UQMip    byte
	MemSz    uint32 // size of the decompressed payload
	PackedSz uint32 // size of the payload as stored
}

// ParseDDSxHeader reads one DDSx header from the start of b.
func ParseDDSxHeader(b []byte) (DDSxHeader, error) {
	var h DDSxHeader
	if len(b) < DDSxHeaderSize {
		return h, fmt.Errorf("ddsx: header needs %d bytes, have %d", DDSxHeaderSize, len(b))
	}
	copy(h.Label[:], b[0:4])
	copy(h.D3DFmt[:], b[4:8])
	h.Flags = uint32(b[8]) | uint32(b[9])<<8 | uint32(b[10])<<16
	h.CMethod = b[11]
	h.W = binary.LittleEndian.Uint16(b[12:14])
	h.H = binary.LittleEndian.Uint16(b[14:16])
	h.Levels = b[16]
	h.HQLevels = b[17]
	h.Depth = binary.LittleEndian.Uint16(b[18:20])
	h.BPP = binary.LittleEndian.Uint16(b[20:22])
	h.LQMip, h.MQMip = b[22]>>4, b[22]&0x0F
	h.DxtShift, h.UQMip = b[23]>>4, b[23]&0x0F
	h.MemSz = binary.LittleEndian.Uint32(b[24:28])
	h.PackedSz = binary.LittleEndian.Uint32(b[28:32])
	return h, nil
}

// Format returns the pixel format as a string, with trailing spaces kept, so
// BC7 reads as "BC7 ".
func (h DDSxHeader) Format() string {
	return string(h.D3DFmt[:])
}

// Decode decompresses one DDSx payload into raw mip data. The payload is the
// PackedSz bytes that follow the header.
func (h DDSxHeader) Decode(payload []byte) ([]byte, error) {
	switch h.CMethod {
	case ddsxStored:
		return bytes.Clone(payload), nil
	case ddsxOodle:
		return oodle.Decompress(payload, int(h.MemSz))
	case ddsxZSTD:
		dec, err := zstd.NewReader(nil)
		if err != nil {
			return nil, fmt.Errorf("ddsx: new zstd reader: %w", err)
		}
		defer dec.Close()
		return dec.DecodeAll(payload, make([]byte, 0, h.MemSz))
	case ddsxZlib:
		r, err := zlib.NewReader(bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("ddsx: new zlib reader: %w", err)
		}
		defer r.Close()
		return io.ReadAll(r)
	case ddsxLZMA:
		return nil, errors.New("ddsx: lzma payloads are not supported")
	default:
		return nil, fmt.Errorf("ddsx: unknown compression method 0x%02x", h.CMethod)
	}
}

// blockBytes returns the size in bytes of one mip level of the given size.
func (h DDSxHeader) blockBytes(width, height int) int {
	blocks := max(1, (width+3)/4) * max(1, (height+3)/4)
	switch h.Format() {
	case "DXT5", "BC7 ", "ATI2":
		return blocks * 16
	default: // DXT1, ATI1
		return blocks * 8
	}
}

// levelCount is the number of mip levels the header claims. Every texture has
// at least the largest one.
func (h DDSxHeader) levelCount() int { return max(1, int(h.Levels)) }

// mipSizes returns the byte size of every mip level, largest first.
func (h DDSxHeader) mipSizes() []int {
	sizes := make([]int, h.levelCount())
	for i := range sizes {
		sizes[i] = h.blockBytes(max(1, int(h.W)>>i), max(1, int(h.H)>>i))
	}
	return sizes
}

// mipLayout reports how many mip levels a payload of n bytes holds and how
// many bytes those levels take. Some packs store only the top levels and
// follow them with a short trailer, so the header level count can overstate
// what is there.
func (h DDSxHeader) mipLayout(n int) (levels, size int) {
	sizes := h.mipSizes()
	total := 0
	for _, s := range sizes {
		total += s
	}
	if n >= total {
		return len(sizes), total
	}
	acc := 0
	for i, s := range sizes {
		if acc+s > n {
			return i, acc
		}
		acc += s
	}
	return len(sizes), acc
}

// unreverseMips puts the mip levels back in largest first order. Textures
// without the reversed mip flag are returned as they are.
func (h DDSxHeader) unreverseMips(data []byte, size int) []byte {
	if h.Flags&ddsxFlagRevMipOrder == 0 || h.Levels < 2 {
		return data
	}
	sizes := h.mipSizes()
	out := make([]byte, 0, size)
	end := size
	for _, s := range sizes {
		out = append(out, data[end-s:end]...)
		end -= s
	}
	return out
}

// fourCCFormats maps a DDSx format to the DXGI format a DDS DX10 header needs.
// A zero value means the format goes in the FourCC field instead.
var fourCCFormats = map[string]uint32{
	"DXT1": 0,
	"DXT5": 0,
	"ATI1": 0,
	"ATI2": 0,
	"BC7 ": 98, // DXGI_FORMAT_BC7_UNORM
}

// mipChain puts a decoded payload in largest first order and reports how many
// mip levels it holds.
func (h DDSxHeader) mipChain(data []byte) ([]byte, int, error) {
	levels, size := h.mipLayout(len(data))
	if levels == 0 {
		return nil, 0, fmt.Errorf("ddsx: payload of %d bytes is shorter than one %dx%d %s mip level",
			len(data), h.W, h.H, h.Format())
	}
	if levels != h.levelCount() && h.Flags&ddsxFlagRevMipOrder != 0 {
		return nil, 0, errors.New("ddsx: reversed mip order with an incomplete mip chain")
	}
	return h.unreverseMips(data, size)[:size], levels, nil
}

// Image decodes the largest mip level into an RGBA image. DXT1 and DXT5 are
// supported; the other formats need block decoders this package does not
// carry.
func (h DDSxHeader) Image(data []byte) (*image.RGBA, error) {
	var alpha bool
	switch h.Format() {
	case "DXT1":
	case "DXT5":
		alpha = true
	default:
		return nil, fmt.Errorf("%w: %q cannot be decoded to an image", ErrDDSxFormat, h.Format())
	}
	chain, _, err := h.mipChain(data)
	if err != nil {
		return nil, err
	}
	// Only the largest level is wanted, and it comes first.
	return decodeBC(chain, int(h.W), int(h.H), alpha)
}

// DDS wraps decoded mip data in a DDS container. Pass the output of Decode.
func (h DDSxHeader) DDS(data []byte) ([]byte, error) {
	dxgi, ok := fourCCFormats[h.Format()]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrDDSxFormat, h.Format())
	}
	data, levels, err := h.mipChain(data)
	if err != nil {
		return nil, err
	}

	const (
		ddsdCaps        = 0x1
		ddsdHeight      = 0x2
		ddsdWidth       = 0x4
		ddsdPixelFormat = 0x1000
		ddsdMipMapCount = 0x20000
		ddsdLinearSize  = 0x80000

		ddscapsComplex = 0x8
		ddscapsMipMap  = 0x400000
		ddscapsTexture = 0x1000

		ddpfFourCC = 0x4
	)

	flags := uint32(ddsdCaps | ddsdHeight | ddsdWidth | ddsdPixelFormat | ddsdLinearSize)
	caps := uint32(ddscapsTexture)
	if levels > 1 {
		flags |= ddsdMipMapCount
		caps |= ddscapsComplex | ddscapsMipMap
	}

	hdr := make([]byte, 128)
	copy(hdr[0:4], "DDS ")
	binary.LittleEndian.PutUint32(hdr[4:8], 124)
	binary.LittleEndian.PutUint32(hdr[8:12], flags)
	binary.LittleEndian.PutUint32(hdr[12:16], uint32(h.H))
	binary.LittleEndian.PutUint32(hdr[16:20], uint32(h.W))
	binary.LittleEndian.PutUint32(hdr[20:24], uint32(h.blockBytes(int(h.W), int(h.H))))
	binary.LittleEndian.PutUint32(hdr[28:32], uint32(levels))
	binary.LittleEndian.PutUint32(hdr[76:80], 32) // pixel format size
	binary.LittleEndian.PutUint32(hdr[80:84], ddpfFourCC)
	binary.LittleEndian.PutUint32(hdr[108:112], caps)

	if dxgi == 0 {
		copy(hdr[84:88], h.D3DFmt[:])
		return append(hdr, data...), nil
	}

	copy(hdr[84:88], "DX10")
	ext := make([]byte, 20)
	binary.LittleEndian.PutUint32(ext[0:4], dxgi)
	binary.LittleEndian.PutUint32(ext[4:8], 3) // DDS_DIMENSION_TEXTURE2D
	binary.LittleEndian.PutUint32(ext[12:16], 1)
	return append(append(hdr, ext...), data...), nil
}
