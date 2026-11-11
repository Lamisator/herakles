// Package chunker implements FastCDC content-defined chunking with a
// keyed gear table, so chunk boundaries cannot be used to fingerprint data.
package chunker

import (
	"errors"
	"fmt"
	"io"
	"math/bits"
)

// Params configure chunk sizes.
type Params struct {
	Min int `json:"min"`
	Avg int `json:"avg"`
	Max int `json:"max"`
}

// DefaultParams: 512 KiB min, 1 MiB average, 8 MiB max.
var DefaultParams = Params{Min: 512 << 10, Avg: 1 << 20, Max: 8 << 20}

// Validate checks the parameters for consistency.
func (p Params) Validate() error {
	if p.Min < 64 || p.Avg < p.Min || p.Max < p.Avg {
		return fmt.Errorf("invalid chunker params: need 64 <= min <= avg <= max (got %d/%d/%d)", p.Min, p.Avg, p.Max)
	}
	if p.Max > 64<<20 {
		return errors.New("invalid chunker params: max must be <= 64 MiB")
	}
	if p.Avg&(p.Avg-1) != 0 {
		return fmt.Errorf("invalid chunker params: avg must be a power of two (got %d)", p.Avg)
	}
	return nil
}

// Table is a keyed gear table.
type Table [256]uint64

// NewTable derives a gear table from seed using splitmix64.
func NewTable(seed uint64) *Table {
	var t Table
	x := seed ^ 0x6a09e667f3bcc908
	for i := range t {
		x += 0x9e3779b97f4a7c15
		z := x
		z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
		z = (z ^ (z >> 27)) * 0x94d049bb133111eb
		t[i] = z ^ (z >> 31)
	}
	return &t
}

// Chunker splits a stream into content-defined chunks.
type Chunker struct {
	p            Params
	gear         *Table
	maskS, maskL uint64
	rd           io.Reader
	buf          []byte
	start, end   int
	eof          bool
}

func highMask(n int) uint64 {
	if n <= 0 {
		return 0
	}
	if n >= 64 {
		return ^uint64(0)
	}
	return ((uint64(1) << n) - 1) << (64 - n)
}

// New creates a chunker reading from rd.
func New(rd io.Reader, p Params, gear *Table) *Chunker {
	b := bits.TrailingZeros(uint(p.Avg))
	c := &Chunker{p: p, gear: gear, maskS: highMask(b + 2), maskL: highMask(b - 2)}
	c.buf = make([]byte, 2*p.Max)
	c.Reset(rd)
	return c
}

// Reset reuses the chunker (and its buffer) for a new reader.
func (c *Chunker) Reset(rd io.Reader) {
	c.rd, c.start, c.end, c.eof = rd, 0, 0, false
}

func (c *Chunker) fill() error {
	if c.eof {
		return nil
	}
	if c.start > 0 {
		copy(c.buf, c.buf[c.start:c.end])
		c.end -= c.start
		c.start = 0
	}
	for c.end < len(c.buf) {
		n, err := c.rd.Read(c.buf[c.end:])
		c.end += n
		if err == io.EOF {
			c.eof = true
			return nil
		}
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
	}
	return nil
}

// Next returns the next chunk. The returned slice is only valid until the next
// call to Next. At the end of the stream it returns io.EOF.
func (c *Chunker) Next() ([]byte, error) {
	if c.end-c.start < c.p.Max && !c.eof {
		if err := c.fill(); err != nil {
			return nil, err
		}
	}
	data := c.buf[c.start:c.end]
	if len(data) == 0 {
		return nil, io.EOF
	}
	n := c.cut(data)
	c.start += n
	return data[:n], nil
}

func (c *Chunker) cut(b []byte) int {
	n := len(b)
	if n <= c.p.Min {
		return n
	}
	if n > c.p.Max {
		n = c.p.Max
	}
	normal := c.p.Avg
	if n < normal {
		normal = n
	}
	var fp uint64
	g := c.gear
	i := c.p.Min
	for ; i < normal; i++ {
		fp = (fp << 1) + g[b[i]]
		if fp&c.maskS == 0 {
			return i + 1
		}
	}
	for ; i < n; i++ {
		fp = (fp << 1) + g[b[i]]
		if fp&c.maskL == 0 {
			return i + 1
		}
	}
	return n
}
