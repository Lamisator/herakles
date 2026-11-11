package tray

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// The tray icon is the GUI's favicon (a golden shield with a white "H"),
// rendered in code so no image files are needed. A coloured badge in the
// lower right corner shows the state.

var (
	shieldColor = color.NRGBA{0xd4, 0xa0, 0x17, 0xff}
	badgeColors = map[State]color.NRGBA{
		Busy:   {0x2f, 0x6f, 0xde, 0xff},
		Failed: {0xd6, 0x45, 0x45, 0xff},
		Locked: {0xd9, 0x8e, 0x04, 0xff},
	}
)

// shield is the favicon path "M32 4 8 14v16c0 15 10 26 24 30 14-4 24-15
// 24-30V14z" flattened into a polygon (viewBox 0..64).
var shield = func() [][2]float64 {
	pts := [][2]float64{{32, 4}, {8, 14}, {8, 30}}
	pts = append(pts, cubic([2]float64{8, 30}, [2]float64{8, 45}, [2]float64{18, 56}, [2]float64{32, 60})...)
	pts = append(pts, cubic([2]float64{32, 60}, [2]float64{46, 56}, [2]float64{56, 45}, [2]float64{56, 30})...)
	return append(pts, [2]float64{56, 14})
}()

func cubic(p0, p1, p2, p3 [2]float64) [][2]float64 {
	var out [][2]float64
	for i := 1; i <= 16; i++ {
		t := float64(i) / 16
		u := 1 - t
		a, b, c, d := u*u*u, 3*u*u*t, 3*u*t*t, t*t*t
		out = append(out, [2]float64{a*p0[0] + b*p1[0] + c*p2[0] + d*p3[0], a*p0[1] + b*p1[1] + c*p2[1] + d*p3[1]})
	}
	return out
}

func inPolygon(x, y float64, poly [][2]float64) bool {
	in := false
	for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
		xi, yi, xj, yj := poly[i][0], poly[i][1], poly[j][0], poly[j][1]
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			in = !in
		}
	}
	return in
}

// segDist is the distance from (x, y) to the segment a-b.
func segDist(x, y, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	t := math.Max(0, math.Min(1, ((x-ax)*dx+(y-ay)*dy)/(dx*dx+dy*dy)))
	return math.Hypot(x-ax-t*dx, y-ay-t*dy)
}

// sample returns the colour at a point of the 64x64 design.
func sample(x, y float64, s State) color.NRGBA {
	if bc, ok := badgeColors[s]; ok {
		switch d := math.Hypot(x-50, y-50); {
		case d <= 10:
			return bc
		case d <= 14:
			return color.NRGBA{0xff, 0xff, 0xff, 0xff}
		}
	}
	if !inPolygon(x, y, shield) {
		return color.NRGBA{}
	}
	if segDist(x, y, 22, 20, 22, 44) <= 3 || segDist(x, y, 42, 20, 42, 44) <= 3 || segDist(x, y, 22, 32, 42, 32) <= 3 {
		return color.NRGBA{0xff, 0xff, 0xff, 0xff}
	}
	return shieldColor
}

// Render draws the icon at size x size pixels with 4x4 supersampling.
func Render(size int, s State) *image.NRGBA {
	const ss = 4
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	scale := 64 / float64(size)
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					c := sample((float64(px)+(float64(sx)+0.5)/ss)*scale, (float64(py)+(float64(sy)+0.5)/ss)*scale, s)
					al := float64(c.A) / 255
					r += float64(c.R) * al
					g += float64(c.G) * al
					b += float64(c.B) * al
					a += al
				}
			}
			if a == 0 {
				continue
			}
			img.SetNRGBA(px, py, color.NRGBA{uint8(r/a + 0.5), uint8(g/a + 0.5), uint8(b/a + 0.5), uint8(a/(ss*ss)*255 + 0.5)})
		}
	}
	return img
}

// PNG returns the icon as a PNG image.
func PNG(size int, s State) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, Render(size, s))
	return buf.Bytes()
}

// ICO returns a Windows icon file with 16, 32 and 48 pixel images stored
// as 32-bit DIBs (readable by every Windows version's LoadImage).
func ICO(s State) []byte {
	sizes := []int{16, 32, 48}
	var imgs [][]byte
	for _, n := range sizes {
		imgs = append(imgs, dib(Render(n, s)))
	}
	var buf bytes.Buffer
	le := binary.LittleEndian
	_ = binary.Write(&buf, le, [3]uint16{0, 1, uint16(len(sizes))})
	off := 6 + 16*len(sizes)
	for i, n := range sizes {
		_ = binary.Write(&buf, le, struct {
			W, H, Colors, Reserved uint8
			Planes, BPP            uint16
			Size, Offset           uint32
		}{uint8(n), uint8(n), 0, 0, 1, 32, uint32(len(imgs[i])), uint32(off)})
		off += len(imgs[i])
	}
	for _, b := range imgs {
		buf.Write(b)
	}
	return buf.Bytes()
}

// dib encodes an image as BITMAPINFOHEADER + bottom-up BGRA pixels + an
// all-zero AND mask (transparency comes from the alpha channel).
func dib(img *image.NRGBA) []byte {
	n := img.Rect.Dx()
	maskStride := ((n + 31) / 32) * 4
	var buf bytes.Buffer
	le := binary.LittleEndian
	_ = binary.Write(&buf, le, struct {
		Size                  uint32
		W, H                  int32
		Planes, BPP           uint16
		Compression, ImgSize  uint32
		XPPM, YPPM            int32
		ClrUsed, ClrImportant uint32
	}{40, int32(n), int32(2 * n), 1, 32, 0, uint32(n*n*4 + maskStride*n), 0, 0, 0, 0})
	for y := n - 1; y >= 0; y-- {
		for x := 0; x < n; x++ {
			c := img.NRGBAAt(x, y)
			buf.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	buf.Write(make([]byte, maskStride*n))
	return buf.Bytes()
}
