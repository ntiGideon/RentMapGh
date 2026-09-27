// Package ogimage draws the 1200×630 preview WhatsApp, Facebook and X show
// when a listing link is shared: the cover photo with the price, the place
// and the move-in total on a dark band.
package ogimage

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"sync"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Inter at display optical size, instanced from the site's variable font
// (it has the cedi sign).
var (
	//go:embed inter-bold.ttf
	boldTTF []byte
	//go:embed inter-medium.ttf
	mediumTTF []byte
)

const W, H = 1200, 630

// Card is the text on the image.
type Card struct {
	Price  string // "GH₵ 1,200"
	Per    string // "month"
	Title  string // "Chamber and hall · Bomso, Kumasi"
	MoveIn string // "GH₵ 8,450 to move in" (optional)
}

var (
	fontsOnce sync.Once
	bold      *opentype.Font
	medium    *opentype.Font
	fontsErr  error
)

func face(f *opentype.Font, size float64) (font.Face, error) {
	return opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
}

var (
	graphite = color.RGBA{0x25, 0x27, 0x2c, 0xff}
	mint     = color.RGBA{0xb8, 0xf7, 0xe4, 0xff}
	white    = color.RGBA{0xff, 0xff, 0xff, 0xff}
	soft     = color.RGBA{0xe8, 0xea, 0xee, 0xff}
)

// Render draws the card over photo (nil: a plain graphite background) and
// returns a JPEG.
func Render(photo image.Image, c Card) ([]byte, error) {
	fontsOnce.Do(func() {
		if bold, fontsErr = opentype.Parse(boldTTF); fontsErr == nil {
			medium, fontsErr = opentype.Parse(mediumTTF)
		}
	})
	if fontsErr != nil {
		return nil, fmt.Errorf("ogimage: fonts: %w", fontsErr)
	}
	dst := image.NewRGBA(image.Rect(0, 0, W, H))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: graphite}, image.Point{}, draw.Src)
	if photo != nil {
		draw.CatmullRom.Scale(dst, dst.Bounds(), photo, coverCrop(photo.Bounds(), W, H), draw.Src, nil)
		shade(dst, 190, H, 0, 0.88) // readable white text even over a bright wall
	}

	priceFace, err := face(bold, 76)
	if err != nil {
		return nil, err
	}
	perFace, _ := face(medium, 36)
	titleFace, _ := face(medium, 38)
	smallFace, _ := face(medium, 30)
	brandFace, _ := face(bold, 28)

	x := 64
	// Brand chip, top left.
	brand := "RentMap"
	bw := font.MeasureString(brandFace, brand).Ceil()
	roundRect(dst, image.Rect(x-2, 48, x+bw+34, 100), 14, mint)
	text(dst, brandFace, graphite, x+16, 84, brand)

	y := H - 64
	if c.MoveIn != "" {
		text(dst, smallFace, mint, x, y, fit(smallFace, c.MoveIn, W-2*x))
		y -= 58
	}
	if c.Title != "" {
		text(dst, titleFace, soft, x, y, fit(titleFace, c.Title, W-2*x))
		y -= 70
	}
	if c.Price != "" {
		end := text(dst, priceFace, white, x, y, c.Price)
		if c.Per != "" {
			text(dst, perFace, soft, end+14, y, "/ "+c.Per)
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 84}); err != nil {
		return nil, fmt.Errorf("ogimage: encode: %w", err)
	}
	return buf.Bytes(), nil
}

// coverCrop is the centred source rectangle with the target's aspect ratio.
func coverCrop(b image.Rectangle, w, h int) image.Rectangle {
	sw, sh := b.Dx(), b.Dy()
	if sw*h > sh*w { // too wide: trim the sides
		cw := sh * w / h
		x0 := b.Min.X + (sw-cw)/2
		return image.Rect(x0, b.Min.Y, x0+cw, b.Max.Y)
	}
	ch := sw * h / w // too tall: trim top and bottom, keeping a little more of the top
	y0 := b.Min.Y + (sh-ch)*2/5
	return image.Rect(b.Min.X, y0, b.Max.X, y0+ch)
}

// shade darkens rows y0..y1 with a gradient from alpha a0 to a1.
func shade(img *image.RGBA, y0, y1 int, a0, a1 float64) {
	for y := y0; y < y1; y++ {
		t := float64(y-y0) / float64(y1-y0)
		a := a0 + (a1-a0)*(1-(1-t)*(1-t)) // ease-out: dark well before the text
		k := 1 - a
		row := img.Pix[y*img.Stride : y*img.Stride+W*4]
		for i := 0; i < len(row); i += 4 {
			row[i] = uint8(float64(row[i]) * k)
			row[i+1] = uint8(float64(row[i+1]) * k)
			row[i+2] = uint8(float64(row[i+2]) * k)
		}
	}
}

// text draws s with its baseline at y and returns the x where it ends.
func text(dst *image.RGBA, f font.Face, c color.Color, x, y int, s string) int {
	d := font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: f, Dot: fixed.P(x, y)}
	d.DrawString(s)
	return d.Dot.X.Ceil()
}

// fit shortens s with an ellipsis until it fits in max pixels.
func fit(f font.Face, s string, maxW int) string {
	if font.MeasureString(f, s).Ceil() <= maxW {
		return s
	}
	r := []rune(s)
	for len(r) > 1 {
		r = r[:len(r)-1]
		if t := string(r) + "…"; font.MeasureString(f, t).Ceil() <= maxW {
			return t
		}
	}
	return s
}

// roundRect fills r with rounded corners of radius rad.
func roundRect(img *image.RGBA, r image.Rectangle, rad int, c color.RGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			dx, dy := 0, 0
			switch {
			case x < r.Min.X+rad:
				dx = r.Min.X + rad - x
			case x >= r.Max.X-rad:
				dx = x - (r.Max.X - rad - 1)
			}
			switch {
			case y < r.Min.Y+rad:
				dy = r.Min.Y + rad - y
			case y >= r.Max.Y-rad:
				dy = y - (r.Max.Y - rad - 1)
			}
			if dx*dx+dy*dy <= rad*rad {
				img.SetRGBA(x, y, c)
			}
		}
	}
}
