package chunker

import (
	"bytes"
	"crypto/sha256"
	"io"
	"math/rand"
	"testing"
)

var small = Params{Min: 2 << 10, Avg: 8 << 10, Max: 64 << 10}

func chunkAll(t *testing.T, data []byte, p Params, seed uint64) [][]byte {
	t.Helper()
	c := New(bytes.NewReader(data), p, NewTable(seed))
	var out [][]byte
	for {
		ch, err := c.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, append([]byte(nil), ch...))
	}
}

func TestChunkerReassembles(t *testing.T) {
	data := make([]byte, 3<<20)
	rand.New(rand.NewSource(1)).Read(data)
	chunks := chunkAll(t, data, small, 42)
	if !bytes.Equal(bytes.Join(chunks, nil), data) {
		t.Fatal("chunks do not reassemble")
	}
	for i, c := range chunks {
		if len(c) > small.Max {
			t.Fatalf("chunk %d too large: %d", i, len(c))
		}
		if i < len(chunks)-1 && len(c) < small.Min {
			t.Fatalf("chunk %d too small: %d", i, len(c))
		}
	}
	avg := len(data) / len(chunks)
	if avg < small.Avg/2 || avg > small.Avg*3 {
		t.Fatalf("average chunk size %d far from %d", avg, small.Avg)
	}
}

func TestChunkerShiftResistance(t *testing.T) {
	data := make([]byte, 2<<20)
	rand.New(rand.NewSource(2)).Read(data)
	a := chunkAll(t, data, small, 7)
	shifted := append([]byte("inserted prefix bytes"), data...)
	b := chunkAll(t, shifted, small, 7)
	set := map[[32]byte]bool{}
	for _, c := range a {
		set[sha256.Sum256(c)] = true
	}
	common := 0
	for _, c := range b {
		if set[sha256.Sum256(c)] {
			common++
		}
	}
	if common < len(a)-3 {
		t.Fatalf("only %d of %d chunks survived a prefix insertion", common, len(a))
	}
}

func TestChunkerSeedChangesBoundaries(t *testing.T) {
	data := make([]byte, 1<<20)
	rand.New(rand.NewSource(3)).Read(data)
	a := chunkAll(t, data, small, 1)
	b := chunkAll(t, data, small, 2)
	if len(a) == len(b) {
		same := true
		for i := range a {
			if len(a[i]) != len(b[i]) {
				same = false
				break
			}
		}
		if same {
			t.Fatal("different seeds produced identical boundaries")
		}
	}
}

func TestChunkerEmptyAndSmall(t *testing.T) {
	if c := chunkAll(t, nil, small, 1); len(c) != 0 {
		t.Fatal("empty input produced chunks")
	}
	c := chunkAll(t, []byte("tiny"), small, 1)
	if len(c) != 1 || string(c[0]) != "tiny" {
		t.Fatal("small input mishandled")
	}
}

func TestParamsValidate(t *testing.T) {
	if DefaultParams.Validate() != nil {
		t.Fatal("default params invalid")
	}
	for _, p := range []Params{{Min: 10, Avg: 8, Max: 100}, {Min: 64, Avg: 1000, Max: 2000}, {Min: 64, Avg: 128, Max: 100}} {
		if p.Validate() == nil {
			t.Fatalf("%+v accepted", p)
		}
	}
}

func BenchmarkChunker(b *testing.B) {
	data := make([]byte, 32<<20)
	rand.New(rand.NewSource(1)).Read(data)
	b.SetBytes(int64(len(data)))
	tab := NewTable(1)
	for i := 0; i < b.N; i++ {
		c := New(bytes.NewReader(data), DefaultParams, tab)
		for {
			if _, err := c.Next(); err != nil {
				break
			}
		}
	}
}
