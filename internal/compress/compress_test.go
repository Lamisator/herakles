package compress

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestRoundtrip(t *testing.T) {
	text := bytes.Repeat([]byte("herakles backup "), 1000)
	random := make([]byte, 10000)
	rand.New(rand.NewSource(1)).Read(random)
	for _, spec := range []string{"none", "auto", "zstd", "zstd:1", "zstd:4", "auto,zstd:3"} {
		m, err := Parse(spec)
		if err != nil {
			t.Fatal(err)
		}
		for _, in := range [][]byte{text, random, {}} {
			out, err := Decompress(Compress(m, in))
			if err != nil || !bytes.Equal(out, in) {
				t.Fatalf("%s: roundtrip failed: %v", spec, err)
			}
		}
		if m.Algorithm == Zstd && len(Compress(m, text)) > len(text)/10 {
			t.Fatalf("%s: text did not compress", spec)
		}
		if m.Auto && len(Compress(m, random)) != len(random)+1 {
			t.Fatalf("%s: auto mode stored incompressible data compressed", spec)
		}
	}
	for _, bad := range []string{"lz4", "zstd:9", "zstd:x"} {
		if _, err := Parse(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	if _, err := Decompress([]byte{9, 1}); err == nil {
		t.Fatal("unknown algorithm accepted")
	}
}
