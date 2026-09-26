package imaging

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scene is a gradient with a few shapes, so hashes have something to chew on.
func scene(w, h int, shift uint8) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), 120 + shift, 255}
			if (x-w/3)*(x-w/3)+(y-h/2)*(y-h/2) < (h/5)*(h/5) {
				c = color.RGBA{250, 240, 30, 255}
			}
			if x > w*2/3 && y > h/4 && y < h*3/4 {
				c = color.RGBA{20, 20, 20, 255}
			}
			img.Set(x, y, c)
		}
	}
	return img
}

func TestPhotoRenditionsStripMetadata(t *testing.T) {
	src := withOrientation(jpegBytes(t, scene(1200, 900, 0)), 6) // rotate 90° CW, plus a GPS tag
	p, err := DecodePhoto(src, 480)
	require.NoError(t, err)
	w, h := p.Size()
	assert.Equal(t, [2]int{900, 1200}, [2]int{w, h}, "orientation baked in")

	r, err := p.Rendition(800, 82)
	require.NoError(t, err)
	assert.Equal(t, [2]int{600, 800}, [2]int{r.Width, r.Height})
	assert.False(t, bytes.Contains(r.JPEG, []byte("Exif")), "no EXIF in the output")
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(r.JPEG))
	require.NoError(t, err)
	assert.Equal(t, 600, cfg.Width)

	big, err := p.Rendition(4000, 82)
	require.NoError(t, err)
	assert.Equal(t, 900, big.Width, "never upscaled")
}

func TestDecodePhotoRejects(t *testing.T) {
	_, err := DecodePhoto(jpegBytes(t, scene(640, 300, 0)), 480)
	assert.ErrorIs(t, err, ErrTooSmall)
	_, err = DecodePhoto([]byte("GIF89a not really"), 480)
	assert.ErrorIs(t, err, ErrUnsupported)
}

func TestPHash(t *testing.T) {
	orig, err := DecodePhoto(jpegBytes(t, scene(1200, 900, 0)), 100)
	require.NoError(t, err)

	// The same photo, resized and recompressed hard, is still "the same".
	r, err := orig.Rendition(500, 40)
	require.NoError(t, err)
	again, err := DecodePhoto(r.JPEG, 100)
	require.NoError(t, err)
	assert.LessOrEqual(t, Distance(orig.PHash(), again.PHash()), 6)

	// A different picture is far away.
	other, err := DecodePhoto(jpegBytes(t, sample(1200, 900)), 100)
	require.NoError(t, err)
	assert.Greater(t, Distance(orig.PHash(), other.PHash()), 16)
}

func TestBlurHash(t *testing.T) {
	p, err := DecodePhoto(jpegBytes(t, scene(1200, 900, 0)), 100)
	require.NoError(t, err)
	h := p.BlurHash()
	assert.Len(t, h, 28, "4×3 components")
	assert.Equal(t, byte('L'), h[0], "size flag for 4×3") // (4-1) + (3-1)*9 = 21 → 'L'
	for _, c := range h {
		assert.True(t, strings.ContainsRune(base83Chars, c))
	}

	// A flat grey photo: the DC term (characters 2–5) is that grey.
	flat := image.NewRGBA(image.Rect(0, 0, 600, 600))
	for i := range flat.Pix {
		flat.Pix[i] = 128
	}
	fp, err := DecodePhoto(jpegBytes(t, flat), 100)
	require.NoError(t, err)
	dc := 0
	for _, c := range fp.BlurHash()[2:6] {
		dc = dc*83 + strings.IndexRune(base83Chars, c)
	}
	assert.Equal(t, [3]int{128, 128, 128}, [3]int{dc >> 16, dc >> 8 & 255, dc & 255})
}
