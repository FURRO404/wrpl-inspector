// Command oodle decodes one raw Oodle LZ stream.
//
//	oodle <input> <output> <decompressed_size>
//
// The argument order matches the ooz_raw helper it replaces. The input holds
// the raw stream with no size prefix. Trailing input bytes are tolerated.
package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/maxsupermanhd/wrpl-inspector/v3/wtcontent/oodle"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: oodle <input> <output> <decompressed_size>")
		os.Exit(2)
	}

	size, err := strconv.Atoi(os.Args[3])
	if err != nil || size < 0 {
		fmt.Fprintf(os.Stderr, "oodle: bad size %q\n", os.Args[3])
		os.Exit(2)
	}

	src, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "oodle: %v\n", err)
		os.Exit(2)
	}

	dst, err := oodle.Decompress(src, size)
	if err != nil {
		fmt.Fprintf(os.Stderr, "oodle: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(os.Args[2], dst, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "oodle: %v\n", err)
		os.Exit(2)
	}
}
