// Package imaging validates and normalises uploaded photos.
//
// Every accepted image is decoded and re-encoded as JPEG. That strips all
// metadata (EXIF GPS, camera serials), neutralises polyglot files, and bakes
// the EXIF orientation into the pixels so photos never show up sideways.
package imaging

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"

	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

var (
	ErrUnsupported = errors.New("Please upload a JPEG, PNG or WebP photo.")
	ErrTooSmall    = errors.New("That photo is too small to read. Please use a clearer, closer photo.")
	ErrCorrupt     = errors.New("We couldn't read that photo. Please take it again.")
)

// MaxPixels guards against decompression bombs (a tiny file that decodes
// to gigabytes). 50 MP covers every phone camera.
const MaxPixels = 50_000_000

// Normalize returns a JPEG no larger than maxDim on its longest side.
func Normalize(data []byte, maxDim, minDim int) ([]byte, error) {
	img, err := decode(data)
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	if b.Dx() < minDim || b.Dy() < minDim {
		return nil, ErrTooSmall
	}
	img = fit(img, maxDim)
	return encode(img, 85)
}

// Avatar returns a size×size centre-cropped JPEG.
func Avatar(data []byte, size int) ([]byte, error) {
	img, err := decode(data)
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	side := min(b.Dx(), b.Dy())
	if side < 64 {
		return nil, ErrTooSmall
	}
	x0 := b.Min.X + (b.Dx()-side)/2
	y0 := b.Min.Y + (b.Dy()-side)/2
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, image.Rect(x0, y0, x0+side, y0+side), draw.Src, nil)
	return encode(dst, 85)
}

// Sniff reports the detected content type (magic bytes, not the client's claim).
func Sniff(data []byte) string { return http.DetectContentType(data) }

func decode(data []byte) (image.Image, error) {
	var (
		cfg    image.Config
		decode func([]byte) (image.Image, error)
		err    error
	)
	switch Sniff(data) {
	case "image/jpeg":
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(data))
		decode = func(b []byte) (image.Image, error) { return jpeg.Decode(bytes.NewReader(b)) }
	case "image/png":
		cfg, err = png.DecodeConfig(bytes.NewReader(data))
		decode = func(b []byte) (image.Image, error) { return png.Decode(bytes.NewReader(b)) }
	case "image/webp":
		cfg, err = webp.DecodeConfig(bytes.NewReader(data))
		decode = func(b []byte) (image.Image, error) { return webp.Decode(bytes.NewReader(b)) }
	default:
		return nil, ErrUnsupported
	}
	if err != nil {
		return nil, ErrCorrupt
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > MaxPixels {
		return nil, ErrCorrupt
	}
	img, err := decode(data)
	if err != nil {
		return nil, ErrCorrupt
	}
	if o := jpegOrientation(data); o > 1 {
		img = orient(img, o)
	}
	return img, nil
}

func fit(img image.Image, maxDim int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxDim && h <= maxDim {
		return img
	}
	if w >= h {
		h = h * maxDim / w
		w = maxDim
	} else {
		w = w * maxDim / h
		h = maxDim
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

func encode(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, fmt.Errorf("imaging: encode: %w", err)
	}
	return buf.Bytes(), nil
}

// jpegOrientation reads EXIF tag 0x0112 from a JPEG, or returns 0.
func jpegOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 0
	}
	i := 2
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return 0
		}
		marker := b[i+1]
		if marker == 0xDA || marker == 0xD9 { // start of scan / end: no EXIF before pixels
			return 0
		}
		size := int(binary.BigEndian.Uint16(b[i+2:]))
		if size < 2 || i+2+size > len(b) {
			return 0
		}
		seg := b[i+4 : i+2+size]
		if marker == 0xE1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + size
	}
	return 0
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 0
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0
	}
	ifd := int(bo.Uint32(t[4:]))
	if ifd+2 > len(t) {
		return 0
	}
	n := int(bo.Uint16(t[ifd:]))
	for k := 0; k < n; k++ {
		e := ifd + 2 + k*12
		if e+12 > len(t) {
			return 0
		}
		if bo.Uint16(t[e:]) == 0x0112 {
			o := int(bo.Uint16(t[e+8:]))
			if o >= 1 && o <= 8 {
				return o
			}
			return 0
		}
	}
	return 0
}

// orient applies an EXIF orientation (2–8) to the pixels.
func orient(src image.Image, o int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	swap := o >= 5
	dw, dh := w, h
	if swap {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2: // mirror horizontal
				dx, dy = w-1-x, y
			case 3: // rotate 180
				dx, dy = w-1-x, h-1-y
			case 4: // mirror vertical
				dx, dy = x, h-1-y
			case 5: // transpose
				dx, dy = y, x
			case 6: // rotate 90 CW
				dx, dy = h-1-y, x
			case 7: // transverse
				dx, dy = h-1-y, w-1-x
			case 8: // rotate 270 CW
				dx, dy = y, w-1-x
			default:
				dx, dy = x, y
			}
			dst.Set(dx, dy, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}
