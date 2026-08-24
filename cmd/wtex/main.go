// Command wtex reads a War Thunder DDSx texture pack and writes one texture
// out as a DDS file.
//
//	wtex <pack.dxp.bin>                      list the textures in the pack
//	wtex <pack.dxp.bin> <name> <out.dds>     write one texture
//
// The name is the one the listing prints. A name without its "*" or "$" suffix
// also matches. An output name that ends in ".png" gets a PNG instead of a
// DDS; that works for DXT1 and DXT5 textures.
package main

import (
	"fmt"
	"image/png"
	"os"
	"strings"

	"github.com/maxsupermanhd/wrpl-inspector/v3/wtcontent"
)

func main() {
	if len(os.Args) != 2 && len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: wtex <pack.dxp.bin> [<name> <out.dds>]")
		os.Exit(2)
	}

	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		fail(err)
	}
	pack, err := wtcontent.ReadDXP(raw)
	if err != nil {
		fail(err)
	}

	if len(os.Args) == 2 {
		for i, name := range pack.Names() {
			h := pack.Header(i)
			fmt.Printf("%s\t%dx%d\t%s\tmips=%d\t%d bytes\n",
				name, h.W, h.H, h.Format(), h.Levels, h.MemSz)
		}
		return
	}

	out := os.Args[3]
	if strings.HasSuffix(strings.ToLower(out), ".png") {
		writePNG(pack, os.Args[2], out)
		return
	}
	dds, err := pack.Texture(os.Args[2])
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile(out, dds, 0o644); err != nil {
		fail(err)
	}
}

// writePNG decodes one texture and saves it as a PNG.
func writePNG(pack *wtcontent.DXP, name, out string) {
	i := pack.Index(name)
	if i < 0 {
		fail(fmt.Errorf("no texture %q", name))
	}
	data, err := pack.Payload(i)
	if err != nil {
		fail(err)
	}
	img, err := pack.Header(i).Image(data)
	if err != nil {
		fail(err)
	}
	f, err := os.Create(out)
	if err != nil {
		fail(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "wtex: %v\n", err)
	os.Exit(1)
}
