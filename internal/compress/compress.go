// Package compress implements the per-blob compression used by Herakles.
// Every stored blob starts with a one byte header that names the algorithm.
package compress

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// Algorithm identifiers stored in the blob header.
const (
	None byte = 0
	Zstd byte = 1
)

// Mode describes how new blobs are compressed.
type Mode struct {
	Algorithm byte
	Level     int // zstd: 1 (fastest) .. 4 (best)
	Auto      bool
}

// String renders the mode in config syntax.
func (m Mode) String() string {
	if m.Algorithm == None {
		return "none"
	}
	s := "zstd:" + strconv.Itoa(m.Level)
	if m.Auto {
		s = "auto," + s
	}
	return s
}

// Parse parses "none", "zstd", "zstd:N", "auto" or "auto,zstd:N".
// Levels: 1=fastest 2=default 3=better 4=best.
func Parse(s string) (Mode, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	m := Mode{Algorithm: Zstd, Level: 2, Auto: true}
	if s == "" || s == "auto" {
		return m, nil
	}
	if s == "none" || s == "off" {
		return Mode{Algorithm: None}, nil
	}
	m.Auto = false
	if rest, ok := strings.CutPrefix(s, "auto,"); ok {
		m.Auto = true
		s = rest
	}
	alg, lvl, hasLvl := strings.Cut(s, ":")
	if alg != "zstd" {
		return m, fmt.Errorf("unknown compression %q (use none, auto, zstd or zstd:1-4)", s)
	}
	if hasLvl {
		n, err := strconv.Atoi(lvl)
		if err != nil || n < 1 || n > 4 {
			return m, fmt.Errorf("invalid zstd level %q (use 1-4)", lvl)
		}
		m.Level = n
	}
	return m, nil
}

var (
	encMu    sync.Mutex
	encoders = map[int]*zstd.Encoder{}
	decoder  *zstd.Decoder
	decOnce  sync.Once
)

func encoder(level int) *zstd.Encoder {
	encMu.Lock()
	defer encMu.Unlock()
	if e, ok := encoders[level]; ok {
		return e
	}
	e, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.EncoderLevel(level)))
	if err != nil {
		panic(err)
	}
	encoders[level] = e
	return e
}

// Compress encodes data according to mode, prefixed with the header byte.
// In auto mode data that does not shrink is stored uncompressed.
func Compress(m Mode, data []byte) []byte {
	if m.Algorithm == Zstd {
		out := make([]byte, 1, len(data)/2+64)
		out[0] = Zstd
		out = encoder(m.Level).EncodeAll(data, out)
		if !m.Auto || len(out) < len(data)+1 {
			return out
		}
	}
	out := make([]byte, 1+len(data))
	out[0] = None
	copy(out[1:], data)
	return out
}

// Decompress reverses Compress.
func Decompress(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty blob")
	}
	switch data[0] {
	case None:
		return data[1:], nil
	case Zstd:
		decOnce.Do(func() {
			var err error
			decoder, err = zstd.NewReader(nil, zstd.WithDecoderConcurrency(0), zstd.WithDecoderMaxMemory(256<<20))
			if err != nil {
				panic(err)
			}
		})
		return decoder.DecodeAll(data[1:], nil)
	default:
		return nil, fmt.Errorf("unknown compression algorithm %d", data[0])
	}
}
