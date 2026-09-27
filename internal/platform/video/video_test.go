package video

import (
	"bytes"
	"context"
	"image/jpeg"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseProbe(t *testing.T) {
	cases := []struct {
		name string
		json string
		want Info
		err  error
	}{
		{"landscape mp4", `{"format":{"format_name":"mov,mp4,m4a,3gp,3g2,mj2","duration":"12.5"},
			"streams":[{"codec_type":"video","width":1920,"height":1080},{"codec_type":"audio"}]}`,
			Info{Width: 1920, Height: 1080, Duration: 12500 * time.Millisecond, HasAudio: true}, nil},
		{"portrait via display matrix", `{"format":{"format_name":"mov,mp4,m4a,3gp,3g2,mj2","duration":"3"},
			"streams":[{"codec_type":"video","width":1920,"height":1080,"side_data_list":[{"rotation":-90}]}]}`,
			Info{Width: 1080, Height: 1920, Duration: 3 * time.Second}, nil},
		{"portrait via old rotate tag", `{"format":{"format_name":"mov,mp4,m4a,3gp,3g2,mj2","duration":"3"},
			"streams":[{"codec_type":"video","width":1280,"height":720,"tags":{"rotate":"270"}}]}`,
			Info{Width: 720, Height: 1280, Duration: 3 * time.Second}, nil},
		{"upside down stays landscape", `{"format":{"format_name":"matroska,webm","duration":"3"},
			"streams":[{"codec_type":"video","width":1280,"height":720,"side_data_list":[{"rotation":180}]}]}`,
			Info{Width: 1280, Height: 720, Duration: 3 * time.Second}, nil},
		{"cover art is not the video", `{"format":{"format_name":"mov,mp4,m4a,3gp,3g2,mj2","duration":"200"},
			"streams":[{"codec_type":"audio"},{"codec_type":"video","width":600,"height":600,"disposition":{"attached_pic":1}}]}`,
			Info{}, ErrNotVideo},
		{"a photo", `{"format":{"format_name":"image2","duration":"0.04"},"streams":[{"codec_type":"video","width":800,"height":600}]}`,
			Info{}, ErrNotVideo},
		{"a playlist", `{"format":{"format_name":"hls","duration":"30"},"streams":[{"codec_type":"video","width":800,"height":600}]}`,
			Info{}, ErrNotVideo},
		{"no duration", `{"format":{"format_name":"matroska,webm","duration":"N/A"},"streams":[{"codec_type":"video","width":800,"height":600}]}`,
			Info{}, ErrNotVideo},
		{"garbage", `not json`, Info{}, ErrNotVideo},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseProbe([]byte(tc.json))
			if tc.err != nil {
				assert.ErrorIs(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// tool skips the test when ffmpeg isn't installed.
func tool(t *testing.T) *Tool {
	t.Helper()
	tl, err := Find("", "")
	if err != nil {
		t.Skip("ffmpeg/ffprobe not on PATH")
	}
	return tl
}

// PhoneClip writes a short clip that looks like a phone recording: shot
// landscape with a 90° rotation flag (so it plays portrait), with sound and
// a GPS position in the metadata.
func PhoneClip(t testing.TB, ffmpeg, dir string, seconds int) string {
	t.Helper()
	raw, out := filepath.Join(dir, "raw.mp4"), filepath.Join(dir, "phone.mov")
	d := time.Duration(seconds) * time.Second
	run := func(args ...string) {
		b, err := exec.Command(ffmpeg, append([]string{"-hide_banner", "-v", "error", "-y"}, args...)...).CombinedOutput()
		require.NoError(t, err, string(b))
	}
	run("-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30:duration="+secs(d),
		"-f", "lavfi", "-i", "sine=frequency=440:duration="+secs(d),
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-shortest", raw)
	run("-display_rotation", "90", "-i", raw, "-c", "copy",
		"-metadata", "location=+05.6037-000.1870/", "-metadata", "com.apple.quicktime.location.ISO6709=+05.6037-000.1870/", out)
	return out
}

func TestTranscode(t *testing.T) {
	tl := tool(t)
	ctx := context.Background()
	dir := t.TempDir()
	in := PhoneClip(t, tl.FFmpeg, dir, 3)

	info, err := tl.Probe(ctx, in)
	require.NoError(t, err)
	assert.Equal(t, [2]int{720, 1280}, [2]int{info.Width, info.Height}, "rotation applied")
	assert.True(t, info.HasAudio)
	assert.InDelta(t, 3.0, info.Duration.Seconds(), 0.2)

	o720, o480 := filepath.Join(dir, "v720.mp4"), filepath.Join(dir, "v480.mp4")
	require.NoError(t, tl.Transcode(ctx, in, 2*time.Second, info.HasAudio,
		Output{Path: o720, ShortSide: 720, CRF: 26, MaxKbps: 1800},
		Output{Path: o480, ShortSide: 480, CRF: 28, MaxKbps: 800}))

	for path, width := range map[string]int{o720: 720, o480: 480} {
		got, err := tl.Probe(ctx, path)
		require.NoError(t, err)
		assert.Equal(t, width, got.Width, path)
		assert.Zero(t, got.Height%2, "even height for x264")
		assert.Greater(t, got.Height, got.Width, "still portrait")
		assert.InDelta(t, 2.0, got.Duration.Seconds(), 0.2, "trimmed to maxLen")
		assert.True(t, got.HasAudio)

		// No GPS and no rotation flag left anywhere in the file.
		meta, err := exec.Command(tl.FFprobe, "-v", "error", "-show_format", "-show_streams", path).Output()
		require.NoError(t, err)
		lower := strings.ToLower(string(meta))
		assert.NotContains(t, lower, "tag:location", path)
		assert.NotContains(t, lower, "iso6709", path)
		assert.NotContains(t, string(meta), "+05.6037", path)
		assert.NotContains(t, string(meta), "rotation=", path)
	}

	frame, err := tl.Frame(ctx, o720, time.Second)
	require.NoError(t, err)
	img, err := jpeg.Decode(bytes.NewReader(frame))
	require.NoError(t, err)
	assert.Equal(t, 720, img.Bounds().Dx())
}

func TestProbeRefusesNonVideo(t *testing.T) {
	tl := tool(t)
	dir := t.TempDir()
	ctx := context.Background()

	// A still image and a playlist that points at another file.
	img := filepath.Join(dir, "photo.mp4")
	b, err := exec.Command(tl.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=640x480", "-frames:v", "1", "-f", "image2", img).CombinedOutput()
	require.NoError(t, err, string(b))
	_, err = tl.Probe(ctx, img)
	assert.ErrorIs(t, err, ErrNotVideo)

	list := filepath.Join(dir, "evil.mp4")
	writeFile(t, list, "#EXTM3U\n#EXT-X-TARGETDURATION:10\n#EXTINF:10,\n"+img+"\n#EXT-X-ENDLIST\n")
	_, err = tl.Probe(ctx, list)
	assert.ErrorIs(t, err, ErrNotVideo)
}
