package ogimage

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRender(t *testing.T) {
	photo := image.NewRGBA(image.Rect(0, 0, 1600, 1200))
	for y := 0; y < 1200; y++ {
		for x := 0; x < 1600; x++ {
			photo.Set(x, y, color.RGBA{uint8(x / 7), uint8(y / 5), 180, 255})
		}
	}
	card := Card{Price: "GH₵ 1,200", Per: "month", Title: "Self-contained chamber and hall · Bomso, Kumasi", MoveIn: "GH₵ 8,450 to move in · every fee listed"}
	for name, p := range map[string]image.Image{"photo": photo, "plain": nil} {
		b, err := Render(p, card)
		require.NoError(t, err, name)
		img, err := jpeg.Decode(bytes.NewReader(b))
		require.NoError(t, err)
		assert.Equal(t, image.Rect(0, 0, W, H), img.Bounds())
		assert.Less(t, len(b), 300_000, "WhatsApp prefers previews under 300 KB")
		if dir := os.Getenv("OGIMAGE_OUT"); dir != "" {
			_ = os.WriteFile(dir+"/og-"+name+".jpg", b, 0o600)
		}
	}
}

func TestCoverCrop(t *testing.T) {
	assert.Equal(t, image.Rect(200, 0, 1342, 600), coverCrop(image.Rect(0, 0, 1543, 600), 1200, 630).Canon())
	r := coverCrop(image.Rect(0, 0, 900, 1600), 1200, 630)
	assert.Equal(t, 900, r.Dx())
	assert.InDelta(t, 472, r.Dy(), 1)
}

func TestFit(t *testing.T) {
	require.NoError(t, fontsErr)
	Render(nil, Card{}) //nolint:errcheck // loads the fonts
	f, _ := face(medium, 38)
	long := "A very long headline that goes on and on and certainly will not fit across one image"
	got := fit(f, long, 400)
	assert.True(t, len([]rune(got)) < len([]rune(long)))
	assert.Equal(t, "…", string([]rune(got)[len([]rune(got))-1:]))
	assert.Equal(t, "Short", fit(f, "Short", 400))
}
