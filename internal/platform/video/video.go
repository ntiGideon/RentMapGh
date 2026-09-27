// Package video wraps the ffprobe and ffmpeg binaries: check an uploaded
// walk-through video, transcode it to small H.264 MP4s and grab a poster
// frame.
//
// Uploads are untrusted input to a large C program, so every call pins the
// demuxers to the phone-video formats (no HLS/concat playlists that could
// read other files or URLs) and only allows the file protocol.
package video

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

var (
	ErrNotVideo = errors.New("That file isn't a video we can read. Use an MP4, MOV, 3GP or WebM from your phone.")
	ErrNoFFmpeg = errors.New("video: ffmpeg/ffprobe not found")
)

// formats are the demuxers phones and browsers produce. "mov,mp4,..." is
// one demuxer; so is "matroska,webm".
var formats = []string{"mov,mp4,m4a,3gp,3g2,mj2", "matroska,webm"}

const formatWhitelist = "mov,mp4,m4a,3gp,3g2,mj2,matroska,webm"

// Tool runs ffmpeg and ffprobe.
type Tool struct {
	FFmpeg, FFprobe string
	Threads         int // per transcode; leaves CPU for the web server
}

// Find locates the binaries ("" means look on PATH).
func Find(ffmpeg, ffprobe string) (*Tool, error) {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	if ffprobe == "" {
		ffprobe = "ffprobe"
	}
	fm, err1 := exec.LookPath(ffmpeg)
	fp, err2 := exec.LookPath(ffprobe)
	if err1 != nil || err2 != nil {
		return nil, ErrNoFFmpeg
	}
	return &Tool{FFmpeg: fm, FFprobe: fp, Threads: 2}, nil
}

// Info is what Probe learns about a file. Width and Height are as the
// video is shown, i.e. after the phone's rotation flag.
type Info struct {
	Width, Height int
	Duration      time.Duration
	HasAudio      bool
}

type probeOut struct {
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
	} `json:"format"`
	Streams []struct {
		CodecType string            `json:"codec_type"`
		Width     int               `json:"width"`
		Height    int               `json:"height"`
		Tags      map[string]string `json:"tags"`
		SideData  []struct {
			Rotation float64 `json:"rotation"`
		} `json:"side_data_list"`
		Disposition struct {
			AttachedPic int `json:"attached_pic"`
		} `json:"disposition"`
	} `json:"streams"`
}

// Probe reads a file's container, size and length. Anything that isn't a
// phone-style video with a picture and a duration is ErrNotVideo.
func (t *Tool) Probe(ctx context.Context, path string) (Info, error) {
	out, err := t.run(ctx, t.FFprobe, "-v", "error", "-protocol_whitelist", "file",
		"-format_whitelist", formatWhitelist, "-print_format", "json", "-show_format", "-show_streams", "file:"+path)
	if err != nil {
		if ctx.Err() != nil {
			return Info{}, ctx.Err()
		}
		return Info{}, ErrNotVideo
	}
	return parseProbe(out)
}

func parseProbe(out []byte) (Info, error) {
	var p probeOut
	if err := json.Unmarshal(out, &p); err != nil {
		return Info{}, ErrNotVideo
	}
	if !slices.Contains(formats, p.Format.FormatName) {
		return Info{}, ErrNotVideo
	}
	secs, err := strconv.ParseFloat(p.Format.Duration, 64)
	if err != nil || secs <= 0 || math.IsInf(secs, 0) {
		return Info{}, ErrNotVideo
	}
	info := Info{Duration: time.Duration(secs * float64(time.Second))}
	found := false
	for _, s := range p.Streams {
		switch {
		case s.CodecType == "audio":
			info.HasAudio = true
		case s.CodecType == "video" && s.Disposition.AttachedPic == 0 && !found && s.Width > 0 && s.Height > 0:
			found = true
			info.Width, info.Height = s.Width, s.Height
			if quarterTurn(rotation(s.Tags, s.SideData)) {
				info.Width, info.Height = s.Height, s.Width
			}
		}
	}
	if !found {
		return Info{}, ErrNotVideo
	}
	return info, nil
}

// rotation is the display rotation in degrees: newer ffprobe reports a
// display matrix, older files carry a "rotate" tag.
func rotation(tags map[string]string, side []struct {
	Rotation float64 `json:"rotation"`
}) float64 {
	for _, sd := range side {
		if sd.Rotation != 0 {
			return sd.Rotation
		}
	}
	r, _ := strconv.ParseFloat(tags["rotate"], 64)
	return r
}

func quarterTurn(deg float64) bool {
	d := int(math.Round(math.Abs(deg))) % 180
	return d == 90
}

// Output is one rendition: the short side is capped at ShortSide (never
// upscaled), quality is x264 CRF with a bitrate ceiling.
type Output struct {
	Path      string
	ShortSide int
	CRF       int
	MaxKbps   int
}

// Transcode decodes the input once and writes every output as H.264/AAC
// MP4 with the index up front (plays before it's fully downloaded).
// All metadata is dropped — phone videos carry the GPS position of the
// building — and the rotation is applied to the pixels. At most maxLen of
// the video is kept.
func (t *Tool) Transcode(ctx context.Context, in string, maxLen time.Duration, hasAudio bool, outs ...Output) error {
	if len(outs) == 0 {
		return nil
	}
	args := []string{"-hide_banner", "-nostdin", "-v", "error", "-y",
		"-protocol_whitelist", "file", "-format_whitelist", formatWhitelist,
		"-t", secs(maxLen), "-i", "file:" + in} // -t before -i trims every output
	var graph strings.Builder
	graph.WriteString("[0:v:0]")
	if len(outs) > 1 {
		fmt.Fprintf(&graph, "split=%d", len(outs))
		for i := range outs {
			fmt.Fprintf(&graph, "[s%d]", i)
		}
		graph.WriteString(";")
	} else {
		graph.WriteString("null[s0];")
	}
	for i, o := range outs {
		n := strconv.Itoa(o.ShortSide)
		// Portrait: cap the width; landscape: cap the height. -2 keeps the
		// aspect ratio with an even size, which x264 needs.
		fmt.Fprintf(&graph, "[s%d]scale=w='if(lte(iw,ih),trunc(min(%s,iw)/2)*2,-2)':h='if(lte(iw,ih),-2,trunc(min(%s,ih)/2)*2)',setsar=1,format=yuv420p[o%d]", i, n, n, i)
		if i < len(outs)-1 {
			graph.WriteString(";")
		}
	}
	args = append(args, "-filter_complex", graph.String())
	for i, o := range outs {
		args = append(args, "-map", fmt.Sprintf("[o%d]", i))
		if hasAudio {
			args = append(args, "-map", "0:a:0", "-c:a", "aac", "-b:a", "64k", "-ac", "1")
		}
		args = append(args,
			"-c:v", "libx264", "-preset", "veryfast", "-profile:v", "main",
			"-crf", strconv.Itoa(o.CRF), "-maxrate", strconv.Itoa(o.MaxKbps)+"k", "-bufsize", strconv.Itoa(2*o.MaxKbps)+"k",
			"-fpsmax", "30", "-threads", strconv.Itoa(max(1, t.Threads)),
			"-map_metadata", "-1", "-map_chapters", "-1", "-movflags", "+faststart",
			"-f", "mp4", "file:"+o.Path)
	}
	if _, err := t.run(ctx, t.FFmpeg, args...); err != nil {
		return fmt.Errorf("video: transcode: %w", err)
	}
	return nil
}

// Frame returns one frame at `at` as a high-quality JPEG (for the poster).
func (t *Tool) Frame(ctx context.Context, path string, at time.Duration) ([]byte, error) {
	out, err := t.run(ctx, t.FFmpeg, "-hide_banner", "-nostdin", "-v", "error",
		"-protocol_whitelist", "file,pipe", "-format_whitelist", formatWhitelist,
		"-ss", secs(at), "-i", "file:"+path, "-frames:v", "1", "-map_metadata", "-1",
		"-c:v", "mjpeg", "-q:v", "2", "-f", "image2pipe", "pipe:1")
	if err != nil {
		return nil, fmt.Errorf("video: frame: %w", err)
	}
	if len(out) == 0 {
		return nil, errors.New("video: frame: no picture")
	}
	return out, nil
}

func secs(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'f', 3, 64) }

func (t *Tool) run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 500 {
			msg = msg[:500]
		}
		return nil, fmt.Errorf("%w: %s", err, msg)
	}
	return stdout.Bytes(), nil
}
