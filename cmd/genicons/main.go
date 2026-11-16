// genicons renders the PNG app icons (same design as static/icon.svg).
// Run: go run ./cmd/genicons internal/web/static
package main

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
)

func main() {
	dir := os.Args[1]
	for _, size := range []int{192, 512} {
		img := image.NewRGBA(image.Rect(0, 0, size, size))
		s := float64(size) / 64
		rect := func(x0, y0, x1, y1 float64, c color.RGBA) {
			draw.Draw(img, image.Rect(int(x0*s), int(y0*s), int(x1*s), int(y1*s)), &image.Uniform{c}, image.Point{}, draw.Src)
		}
		rect(0, 0, 64, 64, color.RGBA{0x0b, 0x7d, 0x6e, 0xff})
		light := color.RGBA{0xe8, 0xf5, 0xf2, 0xff}
		rect(16, 20, 48, 26, light)
		// nozzle: trapezoid approximated by stacked rows
		for y := 26.0; y < 34; y += 0.25 {
			inset := (y - 26) / 2
			rect(22+inset, y, 42-inset, y+0.25, light)
		}
		rect(30, 34, 34, 38, light)
		rect(22, 40, 42, 44, color.RGBA{0xc9, 0xef, 0xe7, 0xff})
		rect(18, 44, 46, 48, color.RGBA{0x9f, 0xe0, 0xd3, 0xff})
		f, err := os.Create(filepath.Join(dir, "icon-"+strconv.Itoa(size)+".png"))
		if err != nil {
			panic(err)
		}
		if err := png.Encode(f, img); err != nil {
			panic(err)
		}
		f.Close()
	}
}
