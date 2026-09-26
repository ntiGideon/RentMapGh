package imaging

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sample is a w×h image, red in the top-left quadrant, blue elsewhere.
func sample(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{0, 0, 255, 255}
			if x < w/2 && y < h/2 {
				c = color.RGBA{255, 0, 0, 255}
			}
			img.Set(x, y, c)
		}
	}
	return img
}

func jpegBytes(t *testing.T, img image.Image) []byte {
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}))
	return buf.Bytes()
}

// withOrientation splices an EXIF APP1 segment carrying tag 0x0112 = o, plus
// a fake GPS tag, into a JPEG.
func withOrientation(j []byte, o uint16) []byte {
	tiff := []byte("MM\x00\x2a\x00\x00\x00\x08")
	entries := []struct{ tag, typ uint16 }{{0x0112, 3}, {0x8825, 4}} // orientation, GPS IFD pointer
	ifd := make([]byte, 2+len(entries)*12+4)
	binary.BigEndian.PutUint16(ifd, uint16(len(entries)))
	for i, e := range entries {
		p := ifd[2+i*12:]
		binary.BigEndian.PutUint16(p, e.tag)
		binary.BigEndian.PutUint16(p[2:], e.typ)
		binary.BigEndian.PutUint32(p[4:], 1)
		binary.BigEndian.PutUint16(p[8:], o)
	}
	payload := append([]byte("Exif\x00\x00"), append(tiff, ifd...)...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	seg = append(seg, payload...)
	out := append([]byte{}, j[:2]...)
	out = append(out, seg...)
	return append(out, j[2:]...)
}

func decodeJPEG(t *testing.T, b []byte) image.Image {
	img, err := jpeg.Decode(bytes.NewReader(b))
	require.NoError(t, err)
	return img
}

func isRed(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return r > 0xB000 && g < 0x5000 && b < 0x5000
}

func TestNormalizeAppliesOrientationAndStripsExif(t *testing.T) {
	src := withOrientation(jpegBytes(t, sample(400, 200)), 6) // stored landscape, meant portrait
	require.Equal(t, 6, jpegOrientation(src))

	out, err := Normalize(src, 2000, 100)
	require.NoError(t, err)
	assert.Equal(t, 0, jpegOrientation(out), "no EXIF left")
	assert.False(t, bytes.Contains(out, []byte("Exif")), "metadata stripped")

	img := decodeJPEG(t, out)
	assert.Equal(t, 200, img.Bounds().Dx(), "rotated to portrait")
	assert.Equal(t, 400, img.Bounds().Dy())
	// Rotating 90° clockwise moves the red top-left quadrant to the top-right.
	assert.True(t, isRed(img.At(150, 50)))
	assert.False(t, isRed(img.At(50, 50)))
}

func TestNormalizeDownscalesAndConvertsPNG(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, sample(3000, 1500)))
	out, err := Normalize(buf.Bytes(), 2000, 100)
	require.NoError(t, err)
	assert.Equal(t, "image/jpeg", Sniff(out))
	img := decodeJPEG(t, out)
	assert.Equal(t, 2000, img.Bounds().Dx())
	assert.Equal(t, 1000, img.Bounds().Dy())
}

func TestNormalizeRejects(t *testing.T) {
	_, err := Normalize([]byte("%PDF-1.7 not an image"), 2000, 100)
	assert.ErrorIs(t, err, ErrUnsupported)
	_, err = Normalize([]byte("<html><script>alert(1)</script>"), 2000, 100)
	assert.ErrorIs(t, err, ErrUnsupported)
	_, err = Normalize(jpegBytes(t, sample(80, 80)), 2000, 300)
	assert.ErrorIs(t, err, ErrTooSmall)
	truncated := jpegBytes(t, sample(400, 400))[:200]
	_, err = Normalize(truncated, 2000, 100)
	assert.ErrorIs(t, err, ErrCorrupt)
}

func TestAvatarIsSquare(t *testing.T) {
	out, err := Avatar(jpegBytes(t, sample(800, 400)), 256)
	require.NoError(t, err)
	img := decodeJPEG(t, out)
	assert.Equal(t, image.Rect(0, 0, 256, 256), img.Bounds())
}
