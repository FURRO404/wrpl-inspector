package oodle

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func loadBench(b *testing.B, name string) ([]byte, int) {
	dir := os.Getenv("OODLE_CORPUS")
	if dir == "" {
		b.Skip("set OODLE_CORPUS to the powzix/ooz testdata directory")
	}
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		b.Skip(err)
	}
	if binary.LittleEndian.Uint64(raw) < 0x10000000000 {
		return raw[8:], int(binary.LittleEndian.Uint64(raw))
	}
	return raw[4:], int(binary.LittleEndian.Uint32(raw))
}

func benchOne(b *testing.B, name string) {
	src, size := loadBench(b, name)
	b.SetBytes(int64(size))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Decompress(src, size); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkKraken(b *testing.B)    { benchOne(b, "mozilla.kraken") }
func BenchmarkMermaid(b *testing.B)   { benchOne(b, "mozilla.mermaid") }
func BenchmarkSelkie(b *testing.B)    { benchOne(b, "mozilla.selkie") }
func BenchmarkLeviathan(b *testing.B) { benchOne(b, "mozilla.leviathan") }
func BenchmarkWebster(b *testing.B)   { benchOne(b, "webster.leviathan") }
