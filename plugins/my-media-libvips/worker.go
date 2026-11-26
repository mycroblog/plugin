package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"

	mediav1 "github.com/mycroblog/plugin/gen/my/plugin/media/v1"
	"github.com/mycroblog/plugin/internal/media"
)

// The plugin runs libvips in a worker, a process of its own binary for each image: a decoder that crashes takes down
// that call only, a cancelled call kills its worker, and the memory of an image goes back to the system with it.
// The worker reads a job in JSON on its standard input and writes its answer on its standard output.

// workerEnv, set to 1, makes the binary a worker.
const workerEnv = "MY_MEDIA_LIBVIPS_WORKER"

// job is what the plugin asks of a worker: an image to process, or, with Check, the checks of the start.
type job struct {
	Check bool `json:"check,omitempty"`
	// Path is the image, in Format; Dir is the directory of the call, for the outputs.
	Path   string         `json:"path,omitempty"`
	Dir    string         `json:"dir,omitempty"`
	Format mediav1.Format `json:"format,omitempty"`
	// Head is the ProcessHead of the call, in the binary form of protobuf.
	Head        []byte `json:"head,omitempty"`
	WebPQuality int    `json:"webp_quality,omitempty"`
	JPEGQuality int    `json:"jpeg_quality,omitempty"`
	Threads     int    `json:"threads,omitempty"`
}

// answer is what a worker answers: the result of the image, or the failure of its own in Error.
type answer struct {
	Result media.ImageResult `json:"result"`
	Error  string            `json:"error,omitempty"`
	// Of a check: the version of libvips, the formats beyond the built-in ones that it decodes, and what it lacks.
	Version string           `json:"version,omitempty"`
	Formats []mediav1.Format `json:"formats,omitempty"`
	Missing []string         `json:"missing,omitempty"`
}

// runWorker runs one job, and returns the exit status of the process.
func runWorker(stdin io.Reader, stdout io.Writer) int {
	var j job
	var a answer
	if err := json.NewDecoder(stdin).Decode(&j); err != nil {
		a.Error = "read the job: " + err.Error()
	} else if err := vipsInit(j.Threads); err != nil {
		a.Error = err.Error()
	} else if j.Check {
		a = check(j.Dir)
	} else {
		res, err := work(j)
		if r := (*errRefused)(nil); errors.As(err, &r) {
			res = media.ImageResult{Outcome: r.outcome}
		} else if err != nil {
			a.Error = err.Error()
		}
		a.Result = res
	}
	if err := json.NewEncoder(stdout).Encode(a); err != nil {
		fmt.Fprintln(os.Stderr, "my-media-libvips worker:", err)
		return 1
	}
	return 0
}

// minVersion is the oldest libvips the plugin runs with: 8.15 brought the savers' keep, which writes no metadata.
var minVersion = [2]int{8, 15}

// check reports the version of libvips, the operations it lacks, and which samples beyond the built-in formats it
// decodes, written to dir.
func check(dir string) answer {
	major, minor, patch := vipsVersion()
	a := answer{Version: fmt.Sprintf("%d.%d.%d", major, minor, patch)}
	if major < minVersion[0] || major == minVersion[0] && minor < minVersion[1] {
		a.Missing = append(a.Missing, fmt.Sprintf("libvips %d.%d or later", minVersion[0], minVersion[1]))
	}
	for _, op := range []string{"jpegload", "pngload", "gifload", "webpload", "jpegsave", "pngsave", "gifsave", "webpsave"} {
		if !vipsHasOperation(op) {
			a.Missing = append(a.Missing, op)
		}
	}
	if len(a.Missing) > 0 || !vipsHasOperation("heifload") {
		return a
	}
	for _, s := range []struct {
		format mediav1.Format
		file   string
	}{
		{mediav1.Format_FORMAT_AVIF, "sample.avif"},
		{mediav1.Format_FORMAT_HEIC, "sample.heic"},
	} {
		data, err := samples.ReadFile("samples/" + s.file)
		if err != nil {
			continue
		}
		path := filepath.Join(dir, s.file)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			continue
		}
		var ar arena
		im, err := ar.load(int(s.format), path, false)
		if err == nil {
			// min decodes every pixel.
			_, err = im.min()
		}
		ar.free()
		if err == nil {
			a.Formats = append(a.Formats, s.format)
		}
	}
	return a
}

// errRefused carries the outcome of an image that the plugin refuses.
type errRefused struct {
	outcome mediav1.Outcome
	msg     string
}

func (e *errRefused) Error() string { return e.msg }

func refuse(outcome mediav1.Outcome, format string, args ...any) error {
	return &errRefused{outcome, fmt.Sprintf(format, args...)}
}

// decodeError refuses as OUTCOME_CORRUPT a failure of libvips that comes from decoding the image, and returns any
// other, such as a full disk, as it is.
func decodeError(err error) error {
	var v *errVips
	if !errors.As(err, &v) {
		return err
	}
	lower := strings.ToLower(v.msg)
	for _, s := range []string{"load", "jpeg", "png", "gif", "webp", "heif", "truncated", "corrupt", "premature", "decode"} {
		if strings.Contains(lower, s) {
			return refuse(mediav1.Outcome_OUTCOME_CORRUPT, "%s", v.msg)
		}
	}
	return err
}

// Blurhash components, as Mastodon and the server's built-in processor use them.
const blurhashX, blurhashY = 4, 4

// loaders names the format that libvips reads each accepted format with: an APNG is read as its default image, a
// still PNG, as the server reads it.
var loaders = map[mediav1.Format]mediav1.Format{
	mediav1.Format_FORMAT_JPEG: mediav1.Format_FORMAT_JPEG,
	mediav1.Format_FORMAT_PNG:  mediav1.Format_FORMAT_PNG,
	mediav1.Format_FORMAT_APNG: mediav1.Format_FORMAT_PNG,
	mediav1.Format_FORMAT_GIF:  mediav1.Format_FORMAT_GIF,
	mediav1.Format_FORMAT_WEBP: mediav1.Format_FORMAT_WEBP,
	mediav1.Format_FORMAT_AVIF: mediav1.Format_FORMAT_AVIF,
	mediav1.Format_FORMAT_HEIC: mediav1.Format_FORMAT_HEIC,
}

func work(j job) (media.ImageResult, error) {
	head := &mediav1.ProcessHead{}
	if err := proto.Unmarshal(j.Head, head); err != nil {
		return media.ImageResult{}, fmt.Errorf("read the head: %w", err)
	}
	format, ok := loaders[j.Format]
	if !ok {
		return media.ImageResult{}, refuse(mediav1.Outcome_OUTCOME_UNSUPPORTED_FORMAT, "not an accepted format")
	}
	limits := head.GetLimits()
	animatable := format == mediav1.Format_FORMAT_GIF || format == mediav1.Format_FORMAT_WEBP

	// The frames as the file stores them, before libvips reads it.
	var stored frameInfo
	if animatable {
		var err error
		if stored, err = frameSizes(j.Path, format); errors.Is(err, errMalformed) {
			return media.ImageResult{}, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "the frames of the image")
		} else if err != nil {
			return media.ImageResult{}, err
		}
		if err := checkFrames(stored, limits); err != nil {
			return media.ImageResult{}, err
		}
	}

	var ar arena
	defer ar.free()
	// The header only: libvips decodes no pixel before an operation asks for one.
	src, err := ar.load(int(format), j.Path, animatable)
	if err != nil {
		return media.ImageResult{}, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "%v", err)
	}
	info := imageInfo{width: src.width(), height: src.pageHeight(), frames: max(1, src.height()/src.pageHeight())}
	info.pixels = int64(info.width) * int64(info.height)
	if animatable && stored.frames > 1 {
		info.frames = max(info.frames, stored.frames)
		info.pixels = max(info.pixels, stored.pixels)
	}
	if err := info.check(limits); err != nil {
		return media.ImageResult{}, err
	}

	p := pipeline{ar: &ar, dir: j.Dir, head: head, webpQuality: j.WebPQuality, jpegQuality: j.JPEGQuality}
	res := media.ImageResult{Outcome: mediav1.Outcome_OUTCOME_OK}

	// An animation: every frame, as the file shows it, in sRGB, decoded once.
	var roll *vimage
	if info.frames > 1 {
		if roll, err = p.prepare(src, info.height); err != nil {
			return media.ImageResult{}, err
		}
	}

	// The first frame, upright, in sRGB: the source of the still outputs. libvips has turned a HEIF image as the file
	// says already, so its EXIF orientation must not turn it again.
	base := src
	if roll != nil {
		if base, err = roll.crop(0, 0, info.width, info.height); err != nil {
			return media.ImageResult{}, err
		}
	}
	if format != mediav1.Format_FORMAT_AVIF && format != mediav1.Format_FORMAT_HEIC {
		if base, err = base.autorot(); err != nil {
			return media.ImageResult{}, decodeError(err)
		}
	}
	if roll != nil {
		base, err = base.withPages(0)
	} else {
		base, err = p.prepare(base, 0)
	}
	if err != nil {
		return media.ImageResult{}, err
	}
	opaque := true
	if base.hasAlpha() {
		m, err := base.extractBands(3, 1)
		if err != nil {
			return media.ImageResult{}, err
		}
		v, err := m.min()
		if err != nil {
			return media.ImageResult{}, err
		}
		if opaque = v >= 255; opaque {
			if base, err = base.extractBands(0, 3); err != nil {
				return media.ImageResult{}, err
			}
		}
	}
	res.Width, res.Height = int32(base.width()), int32(base.height())

	stills := [3]mediav1.Output{mediav1.Output_OUTPUT_STILL_SMALL, mediav1.Output_OUTPUT_STILL_MEDIUM, mediav1.Output_OUTPUT_STILL_LARGE}
	for i, box := range p.boxes() {
		t := media.Plan(base.width(), base.height(), box, head.GetFocusX(), head.GetFocusY())
		im, err := p.target(base, t)
		if err != nil {
			return media.ImageResult{}, err
		}
		if i == 0 {
			if res.Blurhash, err = blurhash(im); err != nil {
				return media.ImageResult{}, err
			}
		}
		f, saver := stillFormat(i, head.GetPurpose(), opaque)
		path := filepath.Join(j.Dir, "still-"+strconv.Itoa(i)+"."+saver)
		if err := im.save(saver, path, p.quality(saver)); err != nil {
			return media.ImageResult{}, err
		}
		res.Outputs = append(res.Outputs, media.Output{Output: stills[i], Format: f, Width: int32(t.Width), Height: int32(t.Height), Path: path})
	}

	if roll != nil {
		frames := roll.height() / info.height
		animated := [3]mediav1.Output{mediav1.Output_OUTPUT_ANIMATED_SMALL, mediav1.Output_OUTPUT_ANIMATED_MEDIUM, mediav1.Output_OUTPUT_ANIMATED_LARGE}
		for i, box := range p.boxes() {
			t := media.Plan(info.width, info.height, box, head.GetFocusX(), head.GetFocusY())
			// Each frame on its own, so that the kernel of the resize never reaches into the next one.
			scaled := make([]*vimage, frames)
			for k := range frames {
				fr, err := roll.crop(0, k*info.height, info.width, info.height)
				if err != nil {
					return media.ImageResult{}, err
				}
				if scaled[k], err = p.target(fr, t); err != nil {
					return media.ImageResult{}, err
				}
			}
			anim, err := joinPages(scaled)
			if err != nil {
				return media.ImageResult{}, err
			}
			f, saver := mediav1.Format_FORMAT_WEBP, "webp"
			if i == 2 {
				f, saver = mediav1.Format_FORMAT_GIF, "gif"
			}
			path := filepath.Join(j.Dir, "animated-"+strconv.Itoa(i)+"."+saver)
			if err := anim.save(saver, path, p.quality(saver)); err != nil {
				return media.ImageResult{}, err
			}
			res.Outputs = append(res.Outputs, media.Output{Output: animated[i], Format: f, Width: int32(t.Width), Height: int32(t.Height), Path: path})
		}
	}
	return res, nil
}

// pipeline holds what the outputs of one image share.
type pipeline struct {
	ar                       *arena
	dir                      string
	head                     *mediav1.ProcessHead
	webpQuality, jpegQuality int
}

func (p *pipeline) boxes() [3]*mediav1.Box {
	return [3]*mediav1.Box{p.head.GetSmall(), p.head.GetMedium(), p.head.GetLarge()}
}

func (p *pipeline) quality(saver string) int {
	switch saver {
	case "webp":
		return p.webpQuality
	case "jpeg":
		return p.jpegQuality
	}
	return 0
}

// prepare converts an image to 8-bit sRGB, of 3 bands or 4 with alpha, and computes it into a temporary file, which
// decodes it: a failure there is the image's. pageHeight is the height of its frames, or 0 for a still image.
func (p *pipeline) prepare(im *vimage, pageHeight int) (*vimage, error) {
	im, err := im.srgb()
	if err != nil {
		return nil, decodeError(err)
	}
	if im.bands() > 4 {
		if im, err = im.extractBands(0, 4); err != nil {
			return nil, err
		}
	}
	if im, err = im.withPages(pageHeight); err != nil {
		return nil, err
	}
	im, err = im.toTemp()
	if err != nil {
		return nil, decodeError(err)
	}
	return im, nil
}

// target crops and scales an image to t.
func (p *pipeline) target(im *vimage, t media.Target) (*vimage, error) {
	c := t.Crop
	out := im
	var err error
	if c.X != 0 || c.Y != 0 || c.Width != im.width() || c.Height != im.height() {
		if out, err = out.crop(c.X, c.Y, c.Width, c.Height); err != nil {
			return nil, err
		}
	}
	if t.Scaled() {
		if out, err = out.resize(t.Width, t.Height); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// stillFormat returns the format of the still output i (small, medium, large) and its saver: WebP for small and
// medium; for large, PNG for a custom emoji or an image with transparency, and JPEG otherwise.
func stillFormat(i int, purpose mediav1.Purpose, opaque bool) (mediav1.Format, string) {
	switch {
	case i < 2:
		return mediav1.Format_FORMAT_WEBP, "webp"
	case purpose == mediav1.Purpose_PURPOSE_EMOJI || !opaque:
		return mediav1.Format_FORMAT_PNG, "png"
	}
	return mediav1.Format_FORMAT_JPEG, "jpeg"
}

// blurhash encodes the pixels of the small output before they are encoded, its alpha left out.
func blurhash(im *vimage) (string, error) {
	rgb := im
	if im.bands() != 3 {
		var err error
		if rgb, err = im.extractBands(0, 3); err != nil {
			return "", err
		}
	}
	px, err := rgb.rgb()
	if err != nil {
		return "", err
	}
	w, h := rgb.width(), rgb.height()
	if len(px) != w*h*3 {
		return "", fmt.Errorf("blurhash: %d bytes of pixels for %d×%d", len(px), w, h)
	}
	return media.Blurhash(px, w, h, blurhashX, blurhashY), nil
}

// imageInfo is what the header of an image says, before any pixel is decoded.
type imageInfo struct {
	// width and height are those of the canvas, which every frame is drawn on.
	width, height int
	frames        int
	// pixels is the sum of the pixels of every frame.
	pixels int64
}

// compositedPixelsFactor times max_pixels bounds the frames × the canvas of an animation (media.proto, Limits).
const compositedPixelsFactor = 4

// check applies the limits of the head, which the image passed at upload.
func (info imageInfo) check(l *mediav1.Limits) error {
	if s := int(l.GetMaxSide()); s > 0 && (info.width > s || info.height > s) {
		return refuse(mediav1.Outcome_OUTCOME_DIMENSIONS, "%d×%d, at most %d a side", info.width, info.height, s)
	}
	if p := l.GetMaxPixels(); p > 0 {
		if int64(info.width)*int64(info.height) > p || info.pixels > p {
			return refuse(mediav1.Outcome_OUTCOME_DIMENSIONS, "more than %d pixels", p)
		}
		if int64(info.frames)*int64(info.width)*int64(info.height) > compositedPixelsFactor*p {
			return refuse(mediav1.Outcome_OUTCOME_DIMENSIONS, "%d frames of %d×%d", info.frames, info.width, info.height)
		}
	}
	return checkFrames(frameInfo{frames: info.frames, pixels: info.pixels}, l)
}

// checkFrames applies the limits that the frames as the file stores them must pass.
func checkFrames(f frameInfo, l *mediav1.Limits) error {
	if n := int(l.GetMaxFrames()); n > 0 && f.frames > n {
		return refuse(mediav1.Outcome_OUTCOME_DIMENSIONS, "%d frames, at most %d", f.frames, n)
	}
	if p := l.GetMaxPixels(); p > 0 && f.pixels > p {
		return refuse(mediav1.Outcome_OUTCOME_DIMENSIONS, "more than %d pixels in the frames", p)
	}
	return nil
}
