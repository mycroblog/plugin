package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	mediav1 "github.com/mycroblog/plugin/gen/my/plugin/media/v1"
	"github.com/mycroblog/plugin/internal/media"
)

// Blurhash components, as Mastodon and the server's built-in processor use them.
const blurhashX, blurhashY = 4, 4

// Converter implements media.ImageProcessor with the ImageMagick command.
type Converter struct {
	// Magick is the ImageMagick 7 command.
	Magick string
	// SRGBProfile is the ICC profile that images with a profile of their own are converted to.
	SRGBProfile string
	// WebPQuality and JPEGQuality are the qualities of the lossy outputs, from 1 to 100.
	WebPQuality int
	JPEGQuality int
}

// coders names the ImageMagick coder of each accepted format. The plugin always names it, so that ImageMagick never
// guesses the format of an upload from its content.
var coders = map[mediav1.Format]string{
	mediav1.Format_FORMAT_JPEG: "jpeg",
	mediav1.Format_FORMAT_PNG:  "png",
	mediav1.Format_FORMAT_GIF:  "gif",
	mediav1.Format_FORMAT_WEBP: "webp",
}

// errRefused carries the outcome of an image that ImageMagick refuses.
type errRefused struct {
	outcome mediav1.Outcome
	msg     string
}

func (e *errRefused) Error() string { return e.msg }

func refuse(outcome mediav1.Outcome, format string, args ...any) error {
	return &errRefused{outcome, fmt.Sprintf(format, args...)}
}

// Process implements media.ImageProcessor.
func (c *Converter) Process(ctx context.Context, in media.Image) (media.ImageResult, error) {
	res, err := c.process(ctx, in)
	if r := (*errRefused)(nil); errors.As(err, &r) {
		return media.ImageResult{Outcome: r.outcome}, nil
	}
	return res, err
}

func (c *Converter) process(ctx context.Context, in media.Image) (media.ImageResult, error) {
	format, err := sniff(in.Path)
	if err != nil {
		return media.ImageResult{}, err
	}
	coder, ok := coders[format]
	if !ok {
		return media.ImageResult{}, refuse(mediav1.Outcome_OUTCOME_UNSUPPORTED_FORMAT, "not an accepted format")
	}
	src := coder + ":" + in.Path
	limits := in.Head.GetLimits()
	env := limitEnv(limits)

	info, err := c.identify(ctx, env, src, format)
	if err != nil {
		return media.ImageResult{}, err
	}
	if err := info.check(limits); err != nil {
		return media.ImageResult{}, err
	}

	// The first frame, upright, in sRGB, without metadata: the source of the still outputs.
	base := filepath.Join(in.Dir, "base.png")
	args := []string{src}
	if info.frames > 1 {
		args = append(args, "-coalesce", "-delete", "1--1")
	} else {
		args[0] += "[0]"
	}
	args = append(args, "-auto-orient")
	args = append(args, c.toSRGB(info)...)
	args = append(args, "-strip", "-define", "png:exclude-chunks=date,time", "png:"+base)
	if _, err := c.run(ctx, env, args...); err != nil {
		return media.ImageResult{}, err
	}
	out, err := c.run(ctx, env, "identify", "-format", "%w %h %[opaque]", "png:"+base)
	if err != nil {
		return media.ImageResult{}, err
	}
	var w, h int
	var opaque string
	if _, err := fmt.Sscan(string(out), &w, &h, &opaque); err != nil {
		return media.ImageResult{}, fmt.Errorf("identify the base image: %q: %w", out, err)
	}

	head := in.Head
	boxes := [3]*mediav1.Box{head.GetSmall(), head.GetMedium(), head.GetLarge()}
	stills := [3]mediav1.Output{mediav1.Output_OUTPUT_STILL_SMALL, mediav1.Output_OUTPUT_STILL_MEDIUM, mediav1.Output_OUTPUT_STILL_LARGE}
	res := media.ImageResult{Outcome: mediav1.Outcome_OUTCOME_OK, Width: int32(w), Height: int32(h)}

	for i, box := range boxes {
		t := media.Plan(w, h, box, head.GetFocusX(), head.GetFocusY())
		args := append([]string{"png:" + base}, geometry(t)...)
		f, name := c.stillFormat(i, head.GetPurpose(), opaque == "True")
		path := filepath.Join(in.Dir, "still-"+strconv.Itoa(i)+"."+name)
		// The blurhash comes from the pixels of the small output before they are encoded.
		pixels := filepath.Join(in.Dir, "small.rgb")
		if i == 0 {
			args = append(args, "(", "+clone", "-alpha", "off", "-depth", "8", "-write", "rgb:"+pixels, "+delete", ")")
		}
		args = append(args, c.encode(f)...)
		args = append(args, name+":"+path)
		if _, err := c.run(ctx, env, args...); err != nil {
			return media.ImageResult{}, err
		}
		if i == 0 {
			if res.Blurhash, err = blurhash(pixels, t.Width, t.Height); err != nil {
				return media.ImageResult{}, err
			}
		}
		res.Outputs = append(res.Outputs, media.Output{Output: stills[i], Format: f, Width: int32(t.Width), Height: int32(t.Height), Path: path})
	}

	if info.frames > 1 {
		animated := [3]mediav1.Output{mediav1.Output_OUTPUT_ANIMATED_SMALL, mediav1.Output_OUTPUT_ANIMATED_MEDIUM, mediav1.Output_OUTPUT_ANIMATED_LARGE}
		for i, box := range boxes {
			t := media.Plan(info.width, info.height, box, head.GetFocusX(), head.GetFocusY())
			args := []string{src, "-coalesce"}
			args = append(args, c.toSRGB(info)...)
			args = append(args, geometry(t)...)
			args = append(args, "-strip")
			f, name := mediav1.Format_FORMAT_WEBP, "webp"
			if i == 2 {
				f, name = mediav1.Format_FORMAT_GIF, "gif"
				args = append(args, "-dither", "FloydSteinberg", "-layers", "Optimize")
			} else {
				args = append(args, "-quality", strconv.Itoa(c.WebPQuality))
			}
			path := filepath.Join(in.Dir, "animated-"+strconv.Itoa(i)+"."+name)
			if _, err := c.run(ctx, env, append(args, name+":"+path)...); err != nil {
				return media.ImageResult{}, err
			}
			res.Outputs = append(res.Outputs, media.Output{Output: animated[i], Format: f, Width: int32(t.Width), Height: int32(t.Height), Path: path})
		}
	}
	return res, nil
}

// stillFormat returns the format of the still output i (small, medium, large): WebP for small and medium; for large,
// PNG for a custom emoji or an image with transparency, and JPEG otherwise.
func (c *Converter) stillFormat(i int, purpose mediav1.Purpose, opaque bool) (mediav1.Format, string) {
	switch {
	case i < 2:
		return mediav1.Format_FORMAT_WEBP, "webp"
	case purpose == mediav1.Purpose_PURPOSE_EMOJI || !opaque:
		return mediav1.Format_FORMAT_PNG, "png"
	}
	return mediav1.Format_FORMAT_JPEG, "jpeg"
}

func (c *Converter) encode(f mediav1.Format) []string {
	switch f {
	case mediav1.Format_FORMAT_WEBP:
		return []string{"-quality", strconv.Itoa(c.WebPQuality)}
	case mediav1.Format_FORMAT_JPEG:
		return []string{"-alpha", "off", "-quality", strconv.Itoa(c.JPEGQuality), "-sampling-factor", "4:2:0"}
	}
	return []string{"-define", "png:exclude-chunks=date,time"}
}

// toSRGB converts an image with an ICC profile to sRGB through the profile, and any other to sRGB by its colorspace.
func (c *Converter) toSRGB(info imageInfo) []string {
	if info.hasICC {
		return []string{"-profile", c.SRGBProfile}
	}
	return []string{"-colorspace", "sRGB"}
}

// geometry crops and scales to the target, with Lanczos, and sharpens a little after a large reduction.
func geometry(t media.Target) []string {
	cr := t.Crop
	args := []string{"-crop", fmt.Sprintf("%dx%d+%d+%d", cr.Width, cr.Height, cr.X, cr.Y), "+repage"}
	if t.Scaled() {
		args = append(args, "-filter", "Lanczos", "-resize", fmt.Sprintf("%dx%d!", t.Width, t.Height))
		if t.Width*2 < cr.Width {
			args = append(args, "-unsharp", "0x0.75+0.75+0.008")
		}
	}
	return args
}

// imageInfo is what identify reads from the headers of an image, without decoding its pixels.
type imageInfo struct {
	// width and height are those of the canvas, which every frame is drawn on.
	width, height int
	frames        int
	// pixels is the sum of the pixels of every frame.
	pixels int64
	hasICC bool
}

func (c *Converter) identify(ctx context.Context, env []string, src string, format mediav1.Format) (imageInfo, error) {
	out, err := c.run(ctx, env, "identify", "-ping", "-format", "%W %H %w %h [%[profiles]]\n", src)
	if err != nil {
		var r *errRefused
		if errors.As(err, &r) && r.outcome == mediav1.Outcome_OUTCOME_DIMENSIONS {
			return imageInfo{}, err
		}
		return imageInfo{}, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "identify: %v", err)
	}
	var info imageInfo
	for line := range strings.Lines(strings.TrimSpace(string(out))) {
		var cw, ch, w, h int
		var profiles string
		if _, err := fmt.Sscan(line, &cw, &ch, &w, &h, &profiles); err != nil {
			return imageInfo{}, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "identify: %q", line)
		}
		if info.frames == 0 {
			info.width, info.height = max(cw, w), max(ch, h)
			info.hasICC = strings.Contains(profiles, "icc")
		}
		info.frames++
		info.pixels += int64(w) * int64(h)
	}
	if info.frames == 0 || info.width <= 0 || info.height <= 0 {
		return imageInfo{}, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "identify: no frame")
	}
	if format == mediav1.Format_FORMAT_JPEG || format == mediav1.Format_FORMAT_PNG {
		// An animated PNG is read as a still image, its default image, as the server reads it.
		info.frames, info.pixels = 1, int64(info.width)*int64(info.height)
	}
	return info, nil
}

// compositedPixelsFactor times max_pixels bounds the frames × the canvas of an animation (media.proto, Limits).
const compositedPixelsFactor = 4

// check applies the limits of the head, which the image passed at upload, before any pixel is decoded.
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
	if f := int(l.GetMaxFrames()); f > 0 && info.frames > f {
		return refuse(mediav1.Outcome_OUTCOME_DIMENSIONS, "%d frames, at most %d", info.frames, f)
	}
	return nil
}

// limitEnv bounds ImageMagick itself by the limits, so that an image whose headers lie is refused while it decodes.
func limitEnv(l *mediav1.Limits) []string {
	var env []string
	if s := l.GetMaxSide(); s > 0 {
		env = append(env, fmt.Sprintf("MAGICK_WIDTH_LIMIT=%d", s), fmt.Sprintf("MAGICK_HEIGHT_LIMIT=%d", s))
	}
	if f := l.GetMaxFrames(); f > 0 {
		env = append(env, fmt.Sprintf("MAGICK_LIST_LENGTH_LIMIT=%d", f))
	}
	return env
}

// run runs magick with args, and returns its standard output. A failure that reads as a problem of the image is
// refused with the matching outcome; any other is an error, which the service answers as temporary.
func (c *Converter) run(ctx context.Context, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.Magick, args...)
	cmd.Env = append(os.Environ(), env...)
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
	switch {
	case containsAny(lower, "exceeds limit", "width or height exceeds", "list length exceeds", "too many"):
		return nil, refuse(mediav1.Outcome_OUTCOME_DIMENSIONS, "magick: %s", msg)
	case containsAny(lower, "corrupt", "improper image header", "insufficient image data", "premature end",
		"unexpected end", "not a jpeg", "negative or zero image size", "unable to read image data"):
		return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "magick: %s", msg)
	}
	return nil, fmt.Errorf("magick %s: %w: %s", args[0], err, msg)
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func sniff(path string) (mediav1.Format, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	head := make([]byte, 16)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return 0, err
	}
	return media.SniffImage(head[:n]), nil
}

func blurhash(path string, w, h int) (string, error) {
	rgb, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(rgb) != w*h*3 {
		return "", fmt.Errorf("blurhash: %d bytes of pixels for %d×%d", len(rgb), w, h)
	}
	return media.Blurhash(rgb, w, h, blurhashX, blurhashY), nil
}
