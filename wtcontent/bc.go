package wtcontent

import (
	"fmt"
	"image"
)

// decodeBC decodes DXT1 or DXT5 block compressed data into an RGBA image.
// Both formats carry the same 8 byte color block: two RGB565 colors and
// sixteen 2 bit indices into a four color palette built from them. DXT5 puts
// an 8 byte alpha block in front of it.
func decodeBC(data []byte, w, h int, alpha bool) (*image.RGBA, error) {
	stride := 8
	if alpha {
		stride = 16
	}
	bw, bh := max(1, (w+3)/4), max(1, (h+3)/4)
	if need := bw * bh * stride; len(data) < need {
		return nil, fmt.Errorf("bc: %dx%d needs %d bytes, have %d", w, h, need, len(data))
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for by := range bh {
		for bx := range bw {
			blk := data[(by*bw+bx)*stride:]
			var a [16]byte
			if alpha {
				bc4Block(blk[:8], &a)
				blk = blk[8:]
			} else {
				a = [16]byte{}
			}
			colorBlock(blk[:8], img, bx*4, by*4, w, h, alpha, &a)
		}
	}
	return img, nil
}

// bc4Block expands the 8 byte alpha block of a DXT5 texture: two end points
// and sixteen 3 bit indices into a palette built from them.
func bc4Block(blk []byte, out *[16]byte) {
	var p [8]byte
	p[0], p[1] = blk[0], blk[1]
	if p[0] > p[1] {
		for i := 1; i < 7; i++ {
			p[i+1] = byte((int(p[0])*(7-i) + int(p[1])*i) / 7)
		}
	} else {
		for i := 1; i < 5; i++ {
			p[i+1] = byte((int(p[0])*(5-i) + int(p[1])*i) / 5)
		}
		p[6], p[7] = 0, 255
	}
	bits := uint64(0)
	for i := range 6 {
		bits |= uint64(blk[2+i]) << (8 * i)
	}
	for i := range 16 {
		out[i] = p[(bits>>(3*i))&7]
	}
}

// colorBlock writes one 4x4 color block into img at pixel x, y.
func colorBlock(blk []byte, img *image.RGBA, x, y, w, h int, alpha bool, a *[16]byte) {
	var r, g, b [4]byte
	c0 := uint16(blk[0]) | uint16(blk[1])<<8
	c1 := uint16(blk[2]) | uint16(blk[3])<<8
	r[0], g[0], b[0] = rgb565(c0)
	r[1], g[1], b[1] = rgb565(c1)
	// DXT5 always uses the four color palette. DXT1 uses it only when the
	// first end point is the larger one; otherwise index 3 is transparent.
	punch := !alpha && c0 <= c1
	if punch {
		r[2], g[2], b[2] = mix12(r[0], r[1]), mix12(g[0], g[1]), mix12(b[0], b[1])
		r[3], g[3], b[3] = 0, 0, 0
	} else {
		r[2], g[2], b[2] = mix13(r[0], r[1]), mix13(g[0], g[1]), mix13(b[0], b[1])
		r[3], g[3], b[3] = mix13(r[1], r[0]), mix13(g[1], g[0]), mix13(b[1], b[0])
	}
	bits := uint32(blk[4]) | uint32(blk[5])<<8 | uint32(blk[6])<<16 | uint32(blk[7])<<24
	for py := range 4 {
		if y+py >= h {
			break
		}
		for px := range 4 {
			if x+px >= w {
				continue
			}
			p := py*4 + px
			i := (bits >> (2 * p)) & 3
			av := byte(255)
			switch {
			case alpha:
				av = a[p]
			case punch && i == 3:
				av = 0
			}
			o := img.PixOffset(x+px, y+py)
			img.Pix[o+0] = r[i]
			img.Pix[o+1] = g[i]
			img.Pix[o+2] = b[i]
			img.Pix[o+3] = av
		}
	}
}

// mix13 returns two thirds of a plus one third of b.
func mix13(a, b byte) byte { return byte((2*int(a) + int(b)) / 3) }

// mix12 returns the midpoint of a and b.
func mix12(a, b byte) byte { return byte((int(a) + int(b)) / 2) }

// rgb565 expands a packed 16 bit color to 8 bits per channel.
func rgb565(c uint16) (r, g, b byte) {
	r = byte((c>>11)&0x1F) << 3
	g = byte((c>>5)&0x3F) << 2
	b = byte(c&0x1F) << 3
	return r | r>>5, g | g>>6, b | b>>5
}
