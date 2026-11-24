package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	mediav1 "github.com/mycroblog/plugin/gen/my/plugin/media/v1"
	"github.com/mycroblog/plugin/internal/media"
)

// Remuxer implements media.AVRemuxer with the FFmpeg commands.
type Remuxer struct {
	FFmpeg  string
	FFprobe string
}

// container is how FFmpeg reads and writes one accepted container.
type container struct {
	demuxer, muxer string
	// muxerArgs are the options of the muxer that keep metadata out of the file.
	muxerArgs []string
}

var containers = map[mediav1.Format]container{
	// The mov demuxer reads MP4 and QuickTime alike; the output is an MP4 with its index at the front.
	mediav1.Format_FORMAT_MP4:  {"mov", "mp4", []string{"-movflags", "+faststart"}},
	mediav1.Format_FORMAT_WEBM: {"matroska", "webm", nil},
	mediav1.Format_FORMAT_MP3:  {"mp3", "mp3", []string{"-id3v2_version", "0", "-write_id3v1", "0", "-write_xing", "0"}},
	mediav1.Format_FORMAT_OGG:  {"ogg", "ogg", nil},
}

// The codecs the server accepts (media.proto, OUTCOME_UNSUPPORTED_CODEC), by FFmpeg's names.
var (
	videoCodecs = []string{"h264", "vp8", "vp9", "av1"}
	audioCodecs = []string{"aac", "opus", "vorbis", "mp3"}
)

type errRefused struct {
	outcome mediav1.Outcome
	msg     string
}

func (e *errRefused) Error() string { return e.msg }

func refuse(outcome mediav1.Outcome, format string, args ...any) error {
	return &errRefused{outcome, fmt.Sprintf(format, args...)}
}

// Remux implements media.AVRemuxer.
func (r *Remuxer) Remux(ctx context.Context, in media.AV) (media.AVResult, error) {
	res, err := r.remux(ctx, in)
	if e := (*errRefused)(nil); errors.As(err, &e) {
		return media.AVResult{Outcome: e.outcome}, nil
	}
	return res, err
}

func (r *Remuxer) remux(ctx context.Context, in media.AV) (media.AVResult, error) {
	format, err := sniff(in.Path)
	if err != nil {
		return media.AVResult{}, err
	}
	c, ok := containers[format]
	if !ok {
		return media.AVResult{}, refuse(mediav1.Outcome_OUTCOME_UNSUPPORTED_FORMAT, "not an accepted container")
	}

	p, err := r.probe(ctx, c.demuxer, in.Path)
	if err != nil {
		return media.AVResult{}, err
	}
	if err := p.check(in.Head); err != nil {
		return media.AVResult{}, err
	}

	body := filepath.Join(in.Dir, "body."+c.muxer)
	args := []string{"-nostdin", "-hide_banner", "-v", "error", "-f", c.demuxer, "-i", in.Path,
		// The video streams but attached pictures, and the audio streams; nothing else.
		"-map", "0:V?", "-map", "0:a?", "-c", "copy",
		"-map_metadata", "-1", "-map_metadata:s:v", "-1", "-map_metadata:s:a", "-1", "-map_chapters", "-1",
		"-fflags", "+bitexact", "-flags:v", "+bitexact", "-flags:a", "+bitexact",
	}
	args = append(args, c.muxerArgs...)
	args = append(args, "-f", c.muxer, "-y", body)
	if err := r.ffmpeg(ctx, args...); err != nil {
		return media.AVResult{}, err
	}
	res := media.AVResult{Outcome: mediav1.Outcome_OUTCOME_OK, Outputs: []media.Output{{Output: mediav1.Output_OUTPUT_AV_BODY, Format: format, Path: body}}}

	if cover := p.cover(); cover != nil {
		out, err := r.writeCover(ctx, c.demuxer, in, cover)
		if err != nil {
			return media.AVResult{}, err
		}
		res.Outputs = append(res.Outputs, out)
	}
	return res, nil
}

// writeCover re-encodes the cover picture in its own format, JPEG or PNG, so that it carries no metadata.
func (r *Remuxer) writeCover(ctx context.Context, demuxer string, in media.AV, s *stream) (media.Output, error) {
	format, codec, ext := mediav1.Format_FORMAT_PNG, []string{"-c:v", "png"}, "png"
	if s.CodecName == "mjpeg" {
		format, codec, ext = mediav1.Format_FORMAT_JPEG, []string{"-c:v", "mjpeg", "-q:v", "2", "-pix_fmt", "yuvj444p"}, "jpg"
	}
	path := filepath.Join(in.Dir, "cover."+ext)
	args := []string{"-nostdin", "-hide_banner", "-v", "error", "-f", demuxer, "-i", in.Path,
		"-map", "0:" + strconv.Itoa(s.Index), "-frames:v", "1", "-map_metadata", "-1",
		"-fflags", "+bitexact", "-flags:v", "+bitexact"}
	args = append(args, codec...)
	args = append(args, "-f", "image2", "-update", "1", "-y", path)
	if err := r.ffmpeg(ctx, args...); err != nil {
		return media.Output{}, err
	}
	return media.Output{Output: mediav1.Output_OUTPUT_AV_COVER, Format: format, Width: int32(s.Width), Height: int32(s.Height), Path: path}, nil
}

// probed is what ffprobe reads of a file.
type probed struct {
	Streams []stream `json:"streams"`
	Format  struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

type stream struct {
	Index        int    `json:"index"`
	CodecType    string `json:"codec_type"`
	CodecName    string `json:"codec_name"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	AvgFrameRate string `json:"avg_frame_rate"`
	RFrameRate   string `json:"r_frame_rate"`
	Duration     string `json:"duration"`
	Disposition  struct {
		AttachedPic int `json:"attached_pic"`
	} `json:"disposition"`
}

func (r *Remuxer) probe(ctx context.Context, demuxer, path string) (probed, error) {
	out, err := r.run(ctx, r.FFprobe, "-v", "error", "-f", demuxer, "-show_format", "-show_streams", "-of", "json", path)
	if err != nil {
		var e *errRefused
		if errors.As(err, &e) {
			return probed{}, err
		}
		if ctx.Err() != nil {
			return probed{}, err
		}
		return probed{}, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "ffprobe: %v", err)
	}
	var p probed
	if err := json.Unmarshal(out, &p); err != nil {
		return probed{}, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "ffprobe: %v", err)
	}
	return p, nil
}

// check refuses a file whose streams or length the server does not accept, before anything is written.
func (p probed) check(head *mediav1.RemuxHead) error {
	l := head.GetLimits()
	var video, audio int
	for _, s := range p.Streams {
		switch {
		case s.CodecType == "video" && s.Disposition.AttachedPic == 1:
			// A cover picture, which leaves as its own output.
		case s.CodecType == "video":
			video++
			if !slices.Contains(videoCodecs, s.CodecName) {
				return refuse(mediav1.Outcome_OUTCOME_UNSUPPORTED_CODEC, "video codec %s", s.CodecName)
			}
			if mp := l.GetMaxPixels(); mp > 0 && int64(s.Width)*int64(s.Height) > mp {
				return refuse(mediav1.Outcome_OUTCOME_DIMENSIONS, "%d×%d, more than %d pixels", s.Width, s.Height, mp)
			}
			if mf := l.GetMaxFrameRate(); mf > 0 {
				if fps := max(rate(s.AvgFrameRate), rate(s.RFrameRate)); fps > mf+0.01 {
					return refuse(mediav1.Outcome_OUTCOME_DIMENSIONS, "%.2f frames per second, at most %.2f", fps, mf)
				}
			}
		case s.CodecType == "audio":
			audio++
			if !slices.Contains(audioCodecs, s.CodecName) {
				return refuse(mediav1.Outcome_OUTCOME_UNSUPPORTED_CODEC, "audio codec %s", s.CodecName)
			}
		}
	}
	if video+audio == 0 {
		return refuse(mediav1.Outcome_OUTCOME_CORRUPT, "no video or audio stream")
	}
	limit := l.GetMaxAudioDurationMs()
	if video > 0 || head.GetKind() == mediav1.AVKind_AV_KIND_VIDEO {
		limit = l.GetMaxVideoDurationMs()
	}
	if limit > 0 {
		d := seconds(p.Format.Duration)
		for _, s := range p.Streams {
			d = max(d, seconds(s.Duration))
		}
		if int64(d*1000) > limit {
			return refuse(mediav1.Outcome_OUTCOME_TOO_LONG, "%.1f s, at most %.1f s", d, float64(limit)/1000)
		}
	}
	return nil
}

// cover returns the first attached picture, or nil.
func (p probed) cover() *stream {
	for i, s := range p.Streams {
		if s.CodecType == "video" && s.Disposition.AttachedPic == 1 {
			return &p.Streams[i]
		}
	}
	return nil
}

// rate reads a frame rate such as "30000/1001".
func rate(s string) float64 {
	n, d, ok := strings.Cut(s, "/")
	num, err1 := strconv.ParseFloat(n, 64)
	if !ok {
		if err1 != nil {
			return 0
		}
		return num
	}
	den, err2 := strconv.ParseFloat(d, 64)
	if err1 != nil || err2 != nil || den == 0 {
		return 0
	}
	return num / den
}

func seconds(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

func (r *Remuxer) ffmpeg(ctx context.Context, args ...string) error {
	_, err := r.run(ctx, r.FFmpeg, args...)
	return err
}

// run runs a command and returns its standard output. A failure that reads as a problem of the file is refused as
// corrupt; any other is an error, which the service answers as temporary.
func (r *Remuxer) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	msg := strings.TrimSpace(stderr.String())
	lower := strings.ToLower(msg)
	for _, s := range []string{"invalid data found", "moov atom not found", "ebml header parsing failed", "could not find codec parameters", "error reading header", "truncat"} {
		if strings.Contains(lower, s) {
			return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "%s: %s", filepath.Base(name), msg)
		}
	}
	return nil, fmt.Errorf("%s: %w: %s", filepath.Base(name), err, msg)
}

func sniff(path string) (mediav1.Format, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	head := make([]byte, 64)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return 0, err
	}
	return media.SniffAV(head[:n]), nil
}
