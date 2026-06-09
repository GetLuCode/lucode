// +build ignore

package main

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
)

func main() {
	const size = 64
	img := image.NewNRGBA(image.Rect(0, 0, size, size))

	bg := color.NRGBA{R: 15, G: 15, B: 18, A: 255}
	green := color.NRGBA{R: 0, G: 212, B: 170, A: 255}

	draw.Draw(img, img.Bounds(), &image.Uniform{bg}, image.Point{}, draw.Src)

	// Draw rounded-rectangle border (approximate with solid rect for simplicity)
	drawRoundRect(img, 1, 1, size-2, size-2, 8, color.NRGBA{R: 0, G: 212, B: 170, A: 100})

	// Draw "L" — vertical stroke at x=12..15, y=12..48; horizontal at x=12..28, y=45..48
	fillRect(img, 12, 12, 15, 48, green)
	fillRect(img, 12, 45, 28, 48, green)

	// Draw "u" — left vertical x=32..35 y=24..44; right vertical x=44..47 y=24..44; bottom arc at y=41..44
	fillRect(img, 32, 24, 35, 44, green)
	fillRect(img, 44, 24, 47, 44, green)
	fillRect(img, 32, 41, 47, 44, green)

	f, _ := os.Create("lu-icon.png")
	defer f.Close()
	png.Encode(f, img)
}

func fillRect(img *image.NRGBA, x0, y0, x1, y1 int, c color.NRGBA) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

func drawRoundRect(img *image.NRGBA, x0, y0, x1, y1, r int, c color.NRGBA) {
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			onEdge := x == x0 || x == x1 || y == y0 || y == y1
			if onEdge {
				img.SetNRGBA(x, y, c)
			}
		}
	}
}
