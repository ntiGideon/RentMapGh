package imaging

import (
	"image"
	"math"
	"math/bits"
	"sort"
	"strings"

	"golang.org/x/image/draw"
)

// Photo is a decoded, upright listing photo, ready to be cut into sizes.
type Photo struct {
	img image.Image
}

// Rendition is one stored size of a photo.
type Rendition struct {
	Width, Height int
	JPEG          []byte
}

// DecodePhoto decodes a JPEG/PNG/WebP upload and applies its EXIF
// orientation. Photos smaller than minSide on their short side are refused.
func DecodePhoto(data []byte, minSide int) (*Photo, error) {
	img, err := decode(data)
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	if min(b.Dx(), b.Dy()) < minSide {
		return nil, ErrTooSmall
	}
	return &Photo{img: img}, nil
}

// Size is the upright width and height.
func (p *Photo) Size() (int, int) { return p.img.Bounds().Dx(), p.img.Bounds().Dy() }

// Rendition re-encodes the photo to fit within maxDim×maxDim. Re-encoding
// drops all metadata, EXIF GPS included.
func (p *Photo) Rendition(maxDim, quality int) (Rendition, error) {
	img := fit(p.img, maxDim)
	b, err := encode(img, quality)
	if err != nil {
		return Rendition{}, err
	}
	return Rendition{Width: img.Bounds().Dx(), Height: img.Bounds().Dy(), JPEG: b}, nil
}

// small returns the photo scaled to exactly w×h (aspect ignored), for hashing.
func (p *Photo) small(w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), p.img, p.img.Bounds(), draw.Src, nil)
	return dst
}

// ── Perceptual hash ───────────────────────────────────────────────────────

// PHash is a 64-bit DCT perceptual hash: near-identical photos (resized,
// recompressed, slightly cropped or brightened) differ in only a few bits.
// It powers "you already added this photo" and, later, duplicate-listing
// detection across landlords (ProjectRequirement §6.11).
func (p *Photo) PHash() uint64 {
	const n = 32
	img := p.small(n, n)
	var px [n][n]float64
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			c := img.RGBAAt(x, y)
			px[y][x] = 0.299*float64(c.R) + 0.587*float64(c.G) + 0.114*float64(c.B)
		}
	}
	d := dct2(px)
	// The top-left 8×8 low frequencies, skipping the DC term (overall brightness).
	vals := make([]float64, 0, 64)
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			vals = append(vals, d[y][x])
		}
	}
	sorted := append([]float64(nil), vals[1:]...)
	sort.Float64s(sorted)
	median := sorted[len(sorted)/2]
	var h uint64
	for i, v := range vals {
		if i > 0 && v > median {
			h |= 1 << uint(63-i)
		}
	}
	return h
}

// Distance is the number of differing bits between two hashes. ≤ 6 of 64
// is "the same photo" in practice.
func Distance(a, b uint64) int { return bits.OnesCount64(a ^ b) }

var dctCos = func() (t [32][32]float64) {
	for k := 0; k < 32; k++ {
		for i := 0; i < 32; i++ {
			t[k][i] = math.Cos(math.Pi / 32 * (float64(i) + 0.5) * float64(k))
		}
	}
	return t
}()

// dct2 is a plain 2-D DCT-II (rows, then columns). 32×32 is small enough
// that the O(n³) version costs well under a millisecond.
func dct2(in [32][32]float64) (out [32][32]float64) {
	var rows [32][32]float64
	for y := 0; y < 32; y++ {
		for k := 0; k < 32; k++ {
			s := 0.0
			for x := 0; x < 32; x++ {
				s += in[y][x] * dctCos[k][x]
			}
			rows[y][k] = s
		}
	}
	for x := 0; x < 32; x++ {
		for k := 0; k < 32; k++ {
			s := 0.0
			for y := 0; y < 32; y++ {
				s += rows[y][x] * dctCos[k][y]
			}
			out[k][x] = s
		}
	}
	return out
}

// ── BlurHash ──────────────────────────────────────────────────────────────

// BlurHash encodes a ~30-character placeholder (https://blurha.sh) that the
// browser paints while the real photo loads on a slow connection.
func (p *Photo) BlurHash() string {
	const cx, cy = 4, 3
	w, h := p.Size()
	sw, sh := 32, 32*h/max(w, 1)
	if w < h {
		sw, sh = 32*w/max(h, 1), 32
	}
	sw, sh = max(sw, 1), max(sh, 1)
	img := p.small(sw, sh)

	lin := make([][3]float64, sw*sh)
	for y := 0; y < sh; y++ {
		for x := 0; x < sw; x++ {
			c := img.RGBAAt(x, y)
			lin[y*sw+x] = [3]float64{srgbToLinear(c.R), srgbToLinear(c.G), srgbToLinear(c.B)}
		}
	}
	var factors [cy * cx][3]float64
	for j := 0; j < cy; j++ {
		for i := 0; i < cx; i++ {
			norm := 2.0
			if i == 0 && j == 0 {
				norm = 1
			}
			var r, g, b float64
			for y := 0; y < sh; y++ {
				by := math.Cos(math.Pi * float64(j) * float64(y) / float64(sh))
				for x := 0; x < sw; x++ {
					basis := math.Cos(math.Pi*float64(i)*float64(x)/float64(sw)) * by
					px := lin[y*sw+x]
					r += basis * px[0]
					g += basis * px[1]
					b += basis * px[2]
				}
			}
			scale := norm / float64(sw*sh)
			factors[j*cx+i] = [3]float64{r * scale, g * scale, b * scale}
		}
	}

	var sb strings.Builder
	base83(&sb, (cx-1)+(cy-1)*9, 1)
	maxAC := 0.0
	for _, f := range factors[1:] {
		maxAC = math.Max(maxAC, math.Max(math.Abs(f[0]), math.Max(math.Abs(f[1]), math.Abs(f[2]))))
	}
	quantMax := 0
	if len(factors) > 1 {
		quantMax = int(math.Max(0, math.Min(82, math.Floor(maxAC*166-0.5))))
	}
	acMax := (float64(quantMax) + 1) / 166
	base83(&sb, quantMax, 1)
	dc := factors[0]
	base83(&sb, int(linearToSRGB(dc[0]))<<16|int(linearToSRGB(dc[1]))<<8|int(linearToSRGB(dc[2])), 4)
	for _, f := range factors[1:] {
		q := func(v float64) int {
			return int(math.Max(0, math.Min(18, math.Floor(signPow(v/acMax, 0.5)*9+9.5))))
		}
		base83(&sb, q(f[0])*19*19+q(f[1])*19+q(f[2]), 2)
	}
	return sb.String()
}

const base83Chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz#$%*+,-.:;=?@[]^_{|}~"

func base83(sb *strings.Builder, v, length int) {
	for i := 1; i <= length; i++ {
		d := (v / int(math.Pow(83, float64(length-i)))) % 83
		sb.WriteByte(base83Chars[d])
	}
}

func srgbToLinear(v uint8) float64 {
	f := float64(v) / 255
	if f <= 0.04045 {
		return f / 12.92
	}
	return math.Pow((f+0.055)/1.055, 2.4)
}

func linearToSRGB(v float64) float64 {
	v = math.Max(0, math.Min(1, v))
	if v <= 0.0031308 {
		return math.Round(v * 12.92 * 255)
	}
	return math.Round((1.055*math.Pow(v, 1/2.4) - 0.055) * 255)
}

func signPow(v, e float64) float64 { return math.Copysign(math.Pow(math.Abs(v), e), v) }
