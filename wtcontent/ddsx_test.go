package wtcontent

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// ddsxHeaderBytes builds a header for a texture of the given shape. The
// payload fields stay zero; the tests that need them set them by hand.
func ddsxHeaderBytes(format string, flags uint32, w, h uint16, levels byte) []byte {
	b := make([]byte, DDSxHeaderSize)
	copy(b[0:4], "DDSx")
	copy(b[4:8], format)
	b[8], b[9], b[10] = byte(flags), byte(flags>>8), byte(flags>>16)
	b[11] = ddsxOodle
	binary.LittleEndian.PutUint16(b[12:14], w)
	binary.LittleEndian.PutUint16(b[14:16], h)
	b[16] = levels
	return b
}

func TestParseDDSxHeader(t *testing.T) {
	b := ddsxHeaderBytes("DXT5", ddsxFlagRevMipOrder, 256, 128, 9)
	binary.LittleEndian.PutUint32(b[24:28], 4321)
	binary.LittleEndian.PutUint32(b[28:32], 1234)
	b[22], b[23] = 0x12, 0x34

	h, err := ParseDDSxHeader(b)
	if err != nil {
		t.Fatal(err)
	}
	if h.Format() != "DXT5" {
		t.Errorf("format: got %q, want %q", h.Format(), "DXT5")
	}
	if h.W != 256 || h.H != 128 {
		t.Errorf("size: got %dx%d, want 256x128", h.W, h.H)
	}
	if h.Flags != ddsxFlagRevMipOrder {
		t.Errorf("flags: got 0x%06x, want 0x%06x", h.Flags, ddsxFlagRevMipOrder)
	}
	if h.CMethod != ddsxOodle {
		t.Errorf("cMethod: got 0x%02x, want 0x%02x", h.CMethod, ddsxOodle)
	}
	if h.Levels != 9 {
		t.Errorf("levels: got %d, want 9", h.Levels)
	}
	if h.LQMip != 1 || h.MQMip != 2 || h.DxtShift != 3 || h.UQMip != 4 {
		t.Errorf("mip nibbles: got %d %d %d %d, want 1 2 3 4", h.LQMip, h.MQMip, h.DxtShift, h.UQMip)
	}
	if h.MemSz != 4321 || h.PackedSz != 1234 {
		t.Errorf("sizes: got mem %d packed %d, want 4321 and 1234", h.MemSz, h.PackedSz)
	}

	if _, err := ParseDDSxHeader(b[:DDSxHeaderSize-1]); err == nil {
		t.Error("a short buffer must not parse")
	}
}

func TestMipSizes(t *testing.T) {
	h, err := ParseDDSxHeader(ddsxHeaderBytes("DXT1", 0, 128, 256, 9))
	if err != nil {
		t.Fatal(err)
	}
	want := []int{16384, 4096, 1024, 256, 64, 16, 8, 8, 8}
	got := h.mipSizes()
	if len(got) != len(want) {
		t.Fatalf("levels: got %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("mip %d: got %d bytes, want %d", i, got[i], want[i])
		}
	}
}

// TestMipLayoutPartialChain covers the packs that store only the top mip
// levels and follow them with a short trailer. The DDS level count must
// describe what is there, not what the header claims.
func TestMipLayoutPartialChain(t *testing.T) {
	h, err := ParseDDSxHeader(ddsxHeaderBytes("DXT1", 0, 128, 256, 9))
	if err != nil {
		t.Fatal(err)
	}
	levels, size := h.mipLayout(16396) // one 128x256 mip plus 12 bytes
	if levels != 1 || size != 16384 {
		t.Errorf("partial chain: got %d levels of %d bytes, want 1 of 16384", levels, size)
	}
	levels, size = h.mipLayout(21864)
	if levels != 9 || size != 21864 {
		t.Errorf("full chain: got %d levels of %d bytes, want 9 of 21864", levels, size)
	}
}

func TestUnreverseMips(t *testing.T) {
	// A 8x8 DXT1 texture has three levels of 32, 8 and 8 bytes.
	h, err := ParseDDSxHeader(ddsxHeaderBytes("DXT1", ddsxFlagRevMipOrder, 8, 8, 3))
	if err != nil {
		t.Fatal(err)
	}
	stored := bytes.Join([][]byte{
		bytes.Repeat([]byte{3}, 8),
		bytes.Repeat([]byte{2}, 8),
		bytes.Repeat([]byte{1}, 32),
	}, nil)
	want := bytes.Join([][]byte{
		bytes.Repeat([]byte{1}, 32),
		bytes.Repeat([]byte{2}, 8),
		bytes.Repeat([]byte{3}, 8),
	}, nil)
	if got := h.unreverseMips(stored, len(stored)); !bytes.Equal(got, want) {
		t.Errorf("unreverseMips: got %v, want %v", got, want)
	}
}

func TestDDSHeader(t *testing.T) {
	h, err := ParseDDSxHeader(ddsxHeaderBytes("DXT1", 0, 8, 8, 3))
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.DDS(make([]byte, 48))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 128+48 {
		t.Fatalf("length: got %d, want %d", len(out), 128+48)
	}
	if string(out[0:4]) != "DDS " {
		t.Errorf("magic: got %q", out[0:4])
	}
	if got := binary.LittleEndian.Uint32(out[12:16]); got != 8 {
		t.Errorf("dwHeight: got %d, want 8", got)
	}
	if got := binary.LittleEndian.Uint32(out[16:20]); got != 8 {
		t.Errorf("dwWidth: got %d, want 8", got)
	}
	if got := binary.LittleEndian.Uint32(out[28:32]); got != 3 {
		t.Errorf("dwMipMapCount: got %d, want 3", got)
	}
	if string(out[84:88]) != "DXT1" {
		t.Errorf("fourCC: got %q, want %q", out[84:88], "DXT1")
	}
}

// TestDDSDX10Header checks that BC7 gets the extended header DDS needs for it.
func TestDDSDX10Header(t *testing.T) {
	h, err := ParseDDSxHeader(ddsxHeaderBytes("BC7 ", 0, 4, 4, 1))
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.DDS(make([]byte, 16))
	if err != nil {
		t.Fatal(err)
	}
	if string(out[84:88]) != "DX10" {
		t.Fatalf("fourCC: got %q, want %q", out[84:88], "DX10")
	}
	if got := binary.LittleEndian.Uint32(out[128:132]); got != 98 {
		t.Errorf("dxgiFormat: got %d, want 98", got)
	}
	if got := binary.LittleEndian.Uint32(out[132:136]); got != 3 {
		t.Errorf("resourceDimension: got %d, want 3", got)
	}
	if got := binary.LittleEndian.Uint32(out[140:144]); got != 1 {
		t.Errorf("arraySize: got %d, want 1", got)
	}
}

func TestDDSRejectsRawFormat(t *testing.T) {
	b := ddsxHeaderBytes("", 0, 4, 4, 1)
	binary.LittleEndian.PutUint32(b[4:8], 0x71) // a raw D3DFORMAT, not a FourCC
	h, err := ParseDDSxHeader(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.DDS(make([]byte, 16)); err == nil {
		t.Error("a raw pixel format must not produce a DDS")
	}
}

func TestDecodeStored(t *testing.T) {
	b := ddsxHeaderBytes("DXT1", 0, 4, 4, 1)
	b[11] = ddsxStored
	h, err := ParseDDSxHeader(b)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("eight!!!")
	got, err := h.Decode(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("stored payload: got %q, want %q", got, want)
	}
}
