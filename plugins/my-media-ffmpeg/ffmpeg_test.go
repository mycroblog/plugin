package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	mediav1 "github.com/mycroblog/plugin/gen/my/plugin/media/v1"
	"github.com/mycroblog/plugin/internal/media"
)

// The secrets stand for the metadata a file hides; no output may carry them.
const (
	secretTitle    = "SECRET-TITLE-0451"
	secretLocation = "+35.6895+139.6917/"
	secretComment  = "SECRET-COMMENT-0451"
)

func remuxer(t *testing.T) *Remuxer {
	t.Helper()
	r := &Remuxer{FFmpeg: "ffmpeg", FFprobe: "ffprobe"}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("FFmpeg is not installed")
	}
	if err := r.check(); err != nil {
		t.Skipf("FFmpeg is not usable: %v", err)
	}
	return r
}

// generate runs ffmpeg with args to make a test file named name, and returns its content.
func generate(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("ffmpeg", append(append([]string{"-nostdin", "-v", "error"}, args...), "-y", path)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate %s: %v: %s", name, err, out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func limits() *mediav1.AVLimits {
	return &mediav1.AVLimits{MaxBytes: 100 << 20, MaxVideoDurationMs: 5 * 60 * 1000, MaxAudioDurationMs: 10 * 60 * 1000, MaxPixels: 3840 * 2160, MaxFrameRate: 120}
}

func remux(t *testing.T, r *Remuxer, kind mediav1.AVKind, l *mediav1.AVLimits, data []byte) media.AVResult {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "input")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := r.Remux(context.Background(), media.AV{Head: &mediav1.RemuxHead{Limits: l, Size: int64(len(data)), Kind: kind}, Path: path, Dir: dir})
	if err != nil {
		t.Fatalf("Remux: %v", err)
	}
	return res
}

func outputs(t *testing.T, res media.AVResult) map[mediav1.Output][]byte {
	t.Helper()
	m := map[mediav1.Output][]byte{}
	for _, o := range res.Outputs {
		data, err := os.ReadFile(o.Path)
		if err != nil {
			t.Fatal(err)
		}
		m[o.Output] = data
	}
	return m
}

func noSecrets(t *testing.T, name string, data []byte) {
	t.Helper()
	for _, s := range []string{secretTitle, secretLocation, secretComment, "creation_time", "2026-01-01"} {
		if bytes.Contains(data, []byte(s)) {
			t.Errorf("%s carries %q", name, s)
		}
	}
}

// streams lists the codec type and name of each stream of a file, such as "video:h264 audio:aac".
func streams(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "probe")
	os.WriteFile(path, data, 0o600)
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_type,codec_name", "-of", "csv=p=0:nk=1", path).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	var s []string
	for line := range strings.Lines(strings.TrimSpace(string(out))) {
		name, typ, _ := strings.Cut(strings.TrimSpace(line), ",")
		s = append(s, typ+":"+name)
	}
	return strings.Join(s, " ")
}

var meta = []string{"-metadata", "title=" + secretTitle, "-metadata", "comment=" + secretComment, "-metadata", "creation_time=2026-01-01T00:00:00Z"}

func TestMOV(t *testing.T) {
	r := remuxer(t)
	// A QuickTime file as a phone writes one: H.264 and AAC, a location, a title, a date, and a subtitle track.
	args := []string{"-f", "lavfi", "-i", "testsrc=size=320x240:rate=30:duration=1", "-f", "lavfi", "-i", "sine=duration=1",
		"-f", "lavfi", "-i", "anullsrc=duration=1", "-map", "0", "-map", "1",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-metadata", "location=" + secretLocation}
	args = append(args, meta...)
	data := generate(t, "in.mov", append(args, "-f", "mov")...)
	if !bytes.Contains(data, []byte(secretLocation)) {
		t.Fatal("the test file does not hold its location")
	}

	res := remux(t, r, mediav1.AVKind_AV_KIND_VIDEO, limits(), data)
	if res.Outcome != mediav1.Outcome_OUTCOME_OK || len(res.Outputs) != 1 {
		t.Fatalf("outcome %v with %d outputs, want OK and the body", res.Outcome, len(res.Outputs))
	}
	body := outputs(t, res)[mediav1.Output_OUTPUT_AV_BODY]
	if f := media.SniffAV(body); f != mediav1.Format_FORMAT_MP4 || res.Outputs[0].Format != f {
		t.Errorf("body is %v, declared %v", f, res.Outputs[0].Format)
	}
	if got := streams(t, body); got != "video:h264 audio:aac" {
		t.Errorf("body streams = %q", got)
	}
	noSecrets(t, "body", body)
	if moov, mdat := bytes.Index(body, []byte("moov")), bytes.Index(body, []byte("mdat")); moov < 0 || moov > mdat {
		t.Errorf("moov at %d, mdat at %d: not fast start", moov, mdat)
	}
	again := outputs(t, remux(t, r, mediav1.AVKind_AV_KIND_VIDEO, limits(), data))[mediav1.Output_OUTPUT_AV_BODY]
	if !bytes.Equal(body, again) {
		t.Error("the body differs between two calls")
	}
}

func TestMP3WithCover(t *testing.T) {
	r := remuxer(t)
	cover := generate(t, "cover.jpg", "-f", "lavfi", "-i", "color=red:size=64x48", "-frames:v", "1")
	coverPath := filepath.Join(t.TempDir(), "cover.jpg")
	os.WriteFile(coverPath, cover, 0o600)
	args := []string{"-f", "lavfi", "-i", "sine=duration=2", "-i", coverPath, "-map", "0", "-map", "1",
		"-c:a", "libmp3lame", "-c:v", "copy", "-disposition:v", "attached_pic", "-id3v2_version", "3"}
	data := generate(t, "in.mp3", append(append(args, meta...), "-f", "mp3")...)

	res := remux(t, r, mediav1.AVKind_AV_KIND_AUDIO, limits(), data)
	if res.Outcome != mediav1.Outcome_OUTCOME_OK || len(res.Outputs) != 2 {
		t.Fatalf("outcome %v with %d outputs, want OK, the body, and the cover", res.Outcome, len(res.Outputs))
	}
	out := outputs(t, res)
	body := out[mediav1.Output_OUTPUT_AV_BODY]
	if bytes.HasPrefix(body, []byte("ID3")) || media.SniffAV(body) != mediav1.Format_FORMAT_MP3 {
		t.Errorf("body starts with %q, want an MP3 frame without ID3", body[:4])
	}
	if got := streams(t, body); got != "audio:mp3" {
		t.Errorf("body streams = %q", got)
	}
	noSecrets(t, "body", body)
	c := res.Outputs[1]
	if c.Output != mediav1.Output_OUTPUT_AV_COVER || c.Format != mediav1.Format_FORMAT_JPEG || c.Width != 64 || c.Height != 48 {
		t.Errorf("cover = %v %v %d×%d, want a 64×48 JPEG", c.Output, c.Format, c.Width, c.Height)
	}
	if media.SniffImage(out[mediav1.Output_OUTPUT_AV_COVER]) != mediav1.Format_FORMAT_JPEG {
		t.Error("the cover is not a JPEG")
	}
	noSecrets(t, "cover", out[mediav1.Output_OUTPUT_AV_COVER])
}

func TestOggAndWebM(t *testing.T) {
	r := remuxer(t)
	for _, tt := range []struct {
		name    string
		kind    mediav1.AVKind
		args    []string
		format  mediav1.Format
		streams string
	}{
		{"Ogg Opus", mediav1.AVKind_AV_KIND_AUDIO, []string{"-f", "lavfi", "-i", "sine=duration=1", "-c:a", "libopus", "-f", "ogg"}, mediav1.Format_FORMAT_OGG, "audio:opus"},
		{"Ogg Vorbis", mediav1.AVKind_AV_KIND_AUDIO, []string{"-f", "lavfi", "-i", "sine=duration=1", "-c:a", "libvorbis", "-f", "ogg"}, mediav1.Format_FORMAT_OGG, "audio:vorbis"},
		{"WebM VP9 Opus", mediav1.AVKind_AV_KIND_VIDEO, []string{"-f", "lavfi", "-i", "testsrc=size=160x120:rate=25:duration=1", "-f", "lavfi", "-i", "sine=duration=1",
			"-c:v", "libvpx-vp9", "-b:v", "200k", "-c:a", "libopus", "-f", "webm"}, mediav1.Format_FORMAT_WEBM, "video:vp9 audio:opus"},
		{"M4A", mediav1.AVKind_AV_KIND_AUDIO, []string{"-f", "lavfi", "-i", "sine=duration=1", "-c:a", "aac", "-f", "ipod"}, mediav1.Format_FORMAT_MP4, "audio:aac"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// The metadata goes with the output, before its format: the last two arguments.
			n := len(tt.args) - 2
			args := append(append(append([]string{}, tt.args[:n]...), meta...), tt.args[n:]...)
			data := generate(t, "in", args...)
			if !bytes.Contains(data, []byte(secretTitle)) {
				t.Fatal("the test file does not hold its title")
			}
			res := remux(t, r, tt.kind, limits(), data)
			if res.Outcome != mediav1.Outcome_OUTCOME_OK {
				t.Fatalf("outcome = %v", res.Outcome)
			}
			body := outputs(t, res)[mediav1.Output_OUTPUT_AV_BODY]
			if f := media.SniffAV(body); f != tt.format {
				t.Errorf("body is %v, want %v", f, tt.format)
			}
			if got := streams(t, body); got != tt.streams {
				t.Errorf("body streams = %q, want %q", got, tt.streams)
			}
			noSecrets(t, "body", body)
		})
	}
}

func TestRefused(t *testing.T) {
	r := remuxer(t)
	video := func(args ...string) []byte {
		return generate(t, "in.mp4", append(args, "-f", "mp4")...)
	}
	h264 := video("-f", "lavfi", "-i", "testsrc=size=320x240:rate=30:duration=2", "-c:v", "libx264", "-pix_fmt", "yuv420p")
	short := limits()
	short.MaxVideoDurationMs = 1000
	small := limits()
	small.MaxPixels = 160 * 120
	slow := limits()
	slow.MaxFrameRate = 24

	for _, tt := range []struct {
		name string
		l    *mediav1.AVLimits
		data []byte
		want mediav1.Outcome
	}{
		{"WAV", limits(), generate(t, "in.wav", "-f", "lavfi", "-i", "sine=duration=1", "-f", "wav"), mediav1.Outcome_OUTCOME_UNSUPPORTED_FORMAT},
		{"MPEG-4 Part 2 video", limits(), video("-f", "lavfi", "-i", "testsrc=size=320x240:rate=30:duration=1", "-c:v", "mpeg4"), mediav1.Outcome_OUTCOME_UNSUPPORTED_CODEC},
		{"FLAC in MP4", limits(), generate(t, "in.mp4", "-f", "lavfi", "-i", "sine=duration=1", "-c:a", "flac", "-strict", "-2", "-f", "mp4"), mediav1.Outcome_OUTCOME_UNSUPPORTED_CODEC},
		{"truncated", limits(), h264[:200], mediav1.Outcome_OUTCOME_CORRUPT},
		{"longer than the limit", short, h264, mediav1.Outcome_OUTCOME_TOO_LONG},
		{"more pixels than the limit", small, h264, mediav1.Outcome_OUTCOME_DIMENSIONS},
		{"faster than the limit", slow, h264, mediav1.Outcome_OUTCOME_DIMENSIONS},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if res := remux(t, r, mediav1.AVKind_AV_KIND_VIDEO, tt.l, tt.data); res.Outcome != tt.want || len(res.Outputs) != 0 {
				t.Errorf("outcome = %v with %d outputs, want %v and none", res.Outcome, len(res.Outputs), tt.want)
			}
		})
	}
}
