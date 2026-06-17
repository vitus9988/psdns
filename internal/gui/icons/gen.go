//go:build ignore

// Command gen renders the placeholder psdns tray icons:
//
//	tray.png  — macOS / Linux (32px badge, 4x supersampled)
//	tray.ico  — Windows (16px + 32px entries)
//
// Run it from the repo root after changing the design:
//
//	go run ./internal/gui/icons/gen.go ./internal/gui/icons
//
// The mark is an intentional throwaway placeholder (a rounded indigo badge with
// a white keyhole). Replace tray.png / tray.ico with the real brand artwork
// later — internal/gui/tray.go just embeds whatever bytes live here.
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
)

var (
	brand = color.NRGBA{R: 0x4F, G: 0x46, B: 0xE5, A: 0xFF} // indigo-600
	white = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
)

func main() {
	outDir := "."
	if len(os.Args) > 1 {
		outDir = os.Args[1]
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		log.Fatal(err)
	}

	pngBytes, err := encodePNG(render(32))
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "tray.png"), pngBytes, 0o644); err != nil {
		log.Fatal(err)
	}

	ico, err := encodeICO([]int{16, 32})
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "tray.ico"), ico, 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("wrote tray.png (%d bytes) and tray.ico (%d bytes) to %s", len(pngBytes), len(ico), outDir)
}

// render draws the icon at size×size with 4x supersampling for clean edges.
func render(size int) *image.NRGBA {
	const ss = 4
	s := size * ss
	fs := float64(s)
	hi := image.NewNRGBA(image.Rect(0, 0, s, s))

	margin := fs * 0.06
	radius := fs * 0.28
	cx := fs / 2
	holeCY := fs * 0.42
	holeR := fs * 0.13
	stemTopY := holeCY
	stemBotY := fs * 0.70
	stemHalfTop := holeR * 0.55
	stemHalfBot := holeR * 0.95

	for y := 0; y < s; y++ {
		for x := 0; x < s; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			// Default: transparent, but carry the brand RGB so the
			// anti-aliased outer edge blends to indigo (not black).
			c := color.NRGBA{R: brand.R, G: brand.G, B: brand.B, A: 0}
			if insideRoundRect(fx, fy, margin, margin, fs-margin, fs-margin, radius) {
				c = brand
				inHole := math.Hypot(fx-cx, fy-holeCY) <= holeR
				inStem := false
				if fy >= stemTopY && fy <= stemBotY {
					t := (fy - stemTopY) / (stemBotY - stemTopY)
					half := stemHalfTop + (stemHalfBot-stemHalfTop)*t
					inStem = math.Abs(fx-cx) <= half
				}
				if inHole || inStem {
					c = white
				}
			}
			hi.SetNRGBA(x, y, c)
		}
	}
	return downsample(hi, size, ss)
}

// insideRoundRect reports whether (px,py) is inside the rounded rectangle
// [x0,y0]-[x1,y1] with corner radius r, using the canonical rounded-box SDF.
func insideRoundRect(px, py, x0, y0, x1, y1, r float64) bool {
	hw := (x1 - x0) / 2
	hh := (y1 - y0) / 2
	cx := (x0 + x1) / 2
	cy := (y0 + y1) / 2
	dx := math.Abs(px-cx) - hw + r
	dy := math.Abs(py-cy) - hh + r
	outside := math.Hypot(math.Max(dx, 0), math.Max(dy, 0))
	inside := math.Min(math.Max(dx, dy), 0)
	return outside+inside-r <= 0
}

// downsample box-averages an ss-supersampled image down to size×size.
func downsample(hi *image.NRGBA, size, ss int) *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, size, size))
	n := ss * ss
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a int
			for dy := 0; dy < ss; dy++ {
				for dx := 0; dx < ss; dx++ {
					c := hi.NRGBAAt(x*ss+dx, y*ss+dy)
					r += int(c.R)
					g += int(c.G)
					b += int(c.B)
					a += int(c.A)
				}
			}
			out.SetNRGBA(x, y, color.NRGBA{uint8(r / n), uint8(g / n), uint8(b / n), uint8(a / n)})
		}
	}
	return out
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// encodeICO wraps PNG-encoded images (Vista+ PNG-in-ICO) into a .ico container.
func encodeICO(sizes []int) ([]byte, error) {
	type entry struct {
		size int
		data []byte
	}
	entries := make([]entry, 0, len(sizes))
	for _, sz := range sizes {
		data, err := encodePNG(render(sz))
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry{size: sz, data: data})
	}

	var buf bytes.Buffer
	// ICONDIR header
	_ = binary.Write(&buf, binary.LittleEndian, uint16(0)) // reserved
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1)) // type: icon
	_ = binary.Write(&buf, binary.LittleEndian, uint16(len(entries)))

	offset := 6 + 16*len(entries)
	for _, e := range entries {
		dim := byte(e.size)                                              // 0 means 256; our sizes are <256
		buf.WriteByte(dim)                                               // width
		buf.WriteByte(dim)                                               // height
		buf.WriteByte(0)                                                 // color count
		buf.WriteByte(0)                                                 // reserved
		_ = binary.Write(&buf, binary.LittleEndian, uint16(1))           // planes
		_ = binary.Write(&buf, binary.LittleEndian, uint16(32))          // bpp
		_ = binary.Write(&buf, binary.LittleEndian, uint32(len(e.data))) // size in bytes
		_ = binary.Write(&buf, binary.LittleEndian, uint32(offset))      // offset
		offset += len(e.data)
	}
	for _, e := range entries {
		buf.Write(e.data)
	}
	return buf.Bytes(), nil
}
