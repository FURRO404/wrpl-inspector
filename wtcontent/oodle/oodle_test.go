package oodle

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestReferenceCorpus decodes the powzix/ooz test corpus and compares each
// result against a recorded checksum. The corpus is large, so it lives outside
// this repository. Point OODLE_CORPUS at the directory to run this test:
//
//	git clone --depth 1 https://github.com/powzix/ooz
//	OODLE_CORPUS=ooz/testdata go test ./...
func TestReferenceCorpus(t *testing.T) {
	dir := os.Getenv("OODLE_CORPUS")
	if dir == "" {
		t.Skip("set OODLE_CORPUS to the powzix/ooz testdata directory")
	}

	f, err := os.Open(filepath.Join("testdata", "reference.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.Fields(sc.Text())
		if len(line) != 4 {
			continue
		}
		name, hdrSize, wantSum := line[0], line[1], line[3]
		hdr, err := strconv.Atoi(hdrSize)
		if err != nil {
			t.Fatalf("%s: bad header size: %v", name, err)
		}
		wantLen, err := strconv.Atoi(line[2])
		if err != nil {
			t.Fatalf("%s: bad length: %v", name, err)
		}

		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Skipf("corpus file missing: %v", err)
			}
			// The ooz tool writes the decompressed size in front of the stream.
			var size int
			if hdr == 8 {
				size = int(binary.LittleEndian.Uint64(raw))
			} else {
				size = int(binary.LittleEndian.Uint32(raw))
			}
			if size != wantLen {
				t.Fatalf("size prefix %d, manifest says %d", size, wantLen)
			}

			got, err := DecompressStrict(raw[hdr:], size)
			if err != nil {
				t.Fatalf("decompress: %v", err)
			}
			if len(got) != size {
				t.Fatalf("got %d bytes, want %d", len(got), size)
			}
			sum := sha256.Sum256(got)
			if hex.EncodeToString(sum[:]) != wantSum {
				t.Fatalf("checksum mismatch")
			}
		})
		n++
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("manifest is empty")
	}
}

func TestEmptyOutput(t *testing.T) {
	got, err := Decompress(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d bytes, want 0", len(got))
	}
}

func TestBadHeader(t *testing.T) {
	if _, err := Decompress([]byte{0x00, 0x00, 0x00, 0x00}, 16); err == nil {
		t.Fatal("want an error for a bad block header")
	}
}

func TestUnsupportedCodec(t *testing.T) {
	// Block header for decoder type 5, which is LZNA.
	for _, tc := range []struct {
		name string
		typ  byte
	}{{"LZNA", 5}, {"Bitknit", 11}} {
		_, err := Decompress([]byte{0x8c, tc.typ, 0, 0, 0, 0}, 16)
		if err == nil {
			t.Fatalf("%s: want an error", tc.name)
		}
		if !strings.Contains(err.Error(), tc.name) {
			t.Fatalf("%s: got %v", tc.name, err)
		}
	}
}

func TestTrailingBytesTolerated(t *testing.T) {
	dir := os.Getenv("OODLE_CORPUS")
	if dir == "" {
		t.Skip("set OODLE_CORPUS to the powzix/ooz testdata directory")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "dickens.kraken"))
	if err != nil {
		t.Skipf("corpus file missing: %v", err)
	}
	size := int(binary.LittleEndian.Uint32(raw))
	src := append(append([]byte{}, raw[4:]...), make([]byte, 64)...)

	if _, err := Decompress(src, size); err != nil {
		t.Fatalf("Decompress must ignore trailing bytes: %v", err)
	}
	if _, err := DecompressStrict(src, size); err == nil {
		t.Fatal("DecompressStrict must reject trailing bytes")
	}
}

// TestManyEntropyArrays covers a multi array chunk that declares more than 32
// entropy arrays. The count is a 6 bit field, so it reaches 63. War Thunder
// ships chunks with 37. The decoder must read them, not run off its own array.
func TestManyEntropyArrays(t *testing.T) {
	const nArrays = 63

	// One stored chunk of one byte per array: 0x80 selects chunk type 0 in the
	// short form, 0x01 is the length, then the byte itself.
	src := []byte{0x80 | nArrays}
	for i := 0; i < nArrays; i++ {
		src = append(src, 0x80, 0x01, byte('a'+i%26))
	}
	// Enough trailing bytes for the reads that follow the array loop.
	src = append(src, make([]byte, 64)...)
	s := padded(src)

	scr := scratch{buf: make([]byte, scratchSize+safeSpace), cur: 0, end: scratchSize, pool: &bufPool{}}
	scr.buf = scr.buf[: scratchSize : scratchSize+safeSpace]

	out := make([]byte, 1<<16)
	arrayData := make([]stream, 4)
	arrayLens := make([]int, 4)

	// The crafted input is not a real chunk, so the call fails. It must fail by
	// returning, never by a panic.
	n, _ := krakenDecodeMultiArray(s, 0, len(src), out, 0, len(out),
		arrayData, arrayLens, len(arrayData), true, scr)
	if n >= 0 {
		t.Logf("decoded %d bytes", n)
	}
}

// TestSizeTooLarge checks that asking for more bytes than the stream holds is
// an error, not silently wrong output. Oodle streams do not record their
// decoded size, so a caller that reads the size from the wrong place must hear
// about it.
func TestSizeTooLarge(t *testing.T) {
	dir := os.Getenv("OODLE_CORPUS")
	if dir == "" {
		t.Skip("set OODLE_CORPUS to the powzix/ooz testdata directory")
	}
	for _, name := range []string{"dickens.kraken", "dickens.mermaid", "dickens.selkie", "dickens.leviathan"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Skipf("corpus file missing: %v", err)
		}
		hdr, size := 4, 0
		if binary.LittleEndian.Uint64(raw) < 0x10000000000 {
			hdr = 8
			size = int(binary.LittleEndian.Uint64(raw))
		} else {
			size = int(binary.LittleEndian.Uint32(raw))
		}
		if _, err := Decompress(raw[hdr:], size+4096); err == nil {
			t.Errorf("%s: want an error for an over-large size", name)
		}
	}
}
