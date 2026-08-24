package wtcontent

import (
	"image/color"
	"testing"
)

// bc1Block builds one 8 byte DXT1 block from two packed colors and a fixed
// palette index for all sixteen pixels.
func bc1Block(c0, c1 uint16, idx byte) []byte {
	bits := byte(idx | idx<<2 | idx<<4 | idx<<6)
	return []byte{
		byte(c0), byte(c0 >> 8),
		byte(c1), byte(c1 >> 8),
		bits, bits, bits, bits,
	}
}

const (
	red565   = 0xF800
	blue565  = 0x001F
	white565 = 0xFFFF
	black565 = 0x0000
)

func TestDecodeBC1EndPoints(t *testing.T) {
	for _, tc := range []struct {
		name string
		idx  byte
		want color.RGBA
	}{
		{"first end point", 0, color.RGBA{255, 0, 0, 255}},
		{"second end point", 1, color.RGBA{0, 0, 255, 255}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img, err := decodeBC(bc1Block(red565, blue565, tc.idx), 4, 4, false)
			if err != nil {
				t.Fatal(err)
			}
			if got := img.RGBAAt(1, 1); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDecodeBC1Punchthrough covers the DXT1 mode that trades the fourth
// palette entry for transparency. It applies when the first end point is not
// the larger one.
func TestDecodeBC1Punchthrough(t *testing.T) {
	img, err := decodeBC(bc1Block(black565, white565, 3), 4, 4, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := img.RGBAAt(0, 0); got.A != 0 {
		t.Errorf("alpha: got %d, want 0", got.A)
	}

	// The same block with the end points swapped keeps all four colors.
	img, err = decodeBC(bc1Block(white565, black565, 3), 4, 4, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := img.RGBAAt(0, 0); got.A != 255 {
		t.Errorf("alpha: got %d, want 255", got.A)
	}
}

func TestDecodeBC3Alpha(t *testing.T) {
	// An alpha block whose two end points are 0 and 255, with every pixel on
	// index 1, gives the second end point everywhere.
	alpha := []byte{0, 255, 0x49, 0x92, 0x24, 0x49, 0x92, 0x24}
	blk := append(alpha, bc1Block(red565, blue565, 0)...)
	img, err := decodeBC(blk, 4, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	got := img.RGBAAt(0, 0)
	if got.A != 255 {
		t.Errorf("alpha: got %d, want 255", got.A)
	}
	if (color.RGBA{got.R, got.G, got.B, 255}) != (color.RGBA{255, 0, 0, 255}) {
		t.Errorf("color: got %v, want red", got)
	}
}

// TestDecodeBC1Punchthrough already covers a whole block, so this checks only
// that a size that is not a multiple of four still fills the image.
func TestDecodeBCPartialBlock(t *testing.T) {
	img, err := decodeBC(bc1Block(red565, blue565, 0), 3, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if img.Rect.Dx() != 3 || img.Rect.Dy() != 2 {
		t.Fatalf("size: got %v", img.Rect)
	}
	if got := img.RGBAAt(2, 1); got.A != 255 {
		t.Errorf("the last pixel is unset: %v", got)
	}
}

func TestDecodeBCShortInput(t *testing.T) {
	if _, err := decodeBC(make([]byte, 4), 4, 4, false); err == nil {
		t.Error("a short buffer must not decode")
	}
}

func TestRGB565(t *testing.T) {
	if r, g, b := rgb565(white565); r != 255 || g != 255 || b != 255 {
		t.Errorf("white: got %d %d %d, want 255 255 255", r, g, b)
	}
	if r, g, b := rgb565(black565); r != 0 || g != 0 || b != 0 {
		t.Errorf("black: got %d %d %d, want 0 0 0", r, g, b)
	}
}
