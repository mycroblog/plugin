// Command my-media-imagemagick is a media plugin of mycroblog/my that implements MediaService.Process (images) with
// ImageMagick.
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	mediav1 "github.com/mycroblog/plugin/gen/my/plugin/media/v1"
	"github.com/mycroblog/plugin/internal/media"
	"github.com/mycroblog/plugin/internal/probe"
	"github.com/mycroblog/plugin/internal/server"
)

func main() {
	var cfg server.Config
	cfg.RegisterFlags(flag.CommandLine)
	var opts media.Options
	flag.IntVar(&opts.Concurrency, "concurrency", runtime.NumCPU(), "images processed at once; further calls wait")
	c := &Converter{}
	flag.StringVar(&c.Magick, "magick", "magick", "the ImageMagick 7 command")
	flag.StringVar(&c.SRGBProfile, "srgb-profile", "/usr/share/color/icc/sRGB.icc", "the sRGB ICC profile that images with a profile are converted to")
	flag.IntVar(&c.WebPQuality, "webp-quality", 80, "quality of the WebP outputs, from 1 to 100")
	flag.IntVar(&c.JPEGQuality, "jpeg-quality", 85, "quality of the JPEG outputs, from 1 to 100")
	flag.Parse()

	if err := c.check(); err != nil {
		fmt.Fprintln(os.Stderr, "my-media-imagemagick:", err)
		os.Exit(2)
	}
	opts.Images = c
	fmt.Fprintln(os.Stderr, "my-media-imagemagick: accepts JPEG, PNG, GIF, WebP, and", formatNames(c.inputFormats))

	mux := http.NewServeMux()
	mux.Handle(probe.NewHandler())
	mux.Handle(media.NewHandler(opts))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := server.Run(ctx, cfg, mux); err != nil {
		fmt.Fprintln(os.Stderr, "my-media-imagemagick:", err)
		os.Exit(1)
	}
}

// check refuses to start with a setting that would fail every call.
func (c *Converter) check() error {
	var errs []error
	for name, q := range map[string]int{"-webp-quality": c.WebPQuality, "-jpeg-quality": c.JPEGQuality} {
		if q < 1 || q > 100 {
			errs = append(errs, fmt.Errorf("%s is %d, not from 1 to 100", name, q))
		}
	}
	if fi, err := os.Stat(c.SRGBProfile); err != nil || !fi.Mode().IsRegular() {
		errs = append(errs, fmt.Errorf("-srgb-profile: %s is not a file; install one (icc-profiles-free on Debian) or name it", c.SRGBProfile))
	}
	out, err := exec.Command(c.Magick, "-version").Output()
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("-magick: %s: %w", c.Magick, err))
	case !strings.HasPrefix(string(out), "Version: ImageMagick 7."):
		errs = append(errs, fmt.Errorf("-magick: %s is not ImageMagick 7", c.Magick))
	default:
		formats, _ := exec.Command(c.Magick, "-list", "format").Output()
		for _, f := range []string{"JPEG", "PNG", "GIF", "WEBP"} {
			if !strings.Contains(string(formats), " "+f+"* ") && !strings.Contains(string(formats), " "+f+" ") {
				errs = append(errs, fmt.Errorf("-magick: %s cannot read and write %s", c.Magick, f))
			}
		}
		c.inputFormats = c.decodable()
	}
	return errors.Join(errs...)
}

// samples holds an image of each format beyond the built-in ones that ImageMagick may decode, through libheif and its
// decoders, which an installation may lack or its policy may forbid.
//
//go:embed samples
var samples embed.FS

// decodable returns the formats beyond the built-in ones that the plugin accepts: those of the samples that this
// ImageMagick decodes, and APNG, which the plugin reads itself.
func (c *Converter) decodable() []mediav1.Format {
	dir, err := os.MkdirTemp("", "my-media-imagemagick-")
	if err != nil {
		return []mediav1.Format{mediav1.Format_FORMAT_APNG}
	}
	defer os.RemoveAll(dir)
	var formats []mediav1.Format
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
		// Without -ping, identify decodes every pixel.
		out, err := exec.Command(c.Magick, "identify", "-format", "%w", coders[s.format]+":"+path).Output()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			formats = append(formats, s.format)
		}
	}
	return append(formats, mediav1.Format_FORMAT_APNG)
}

func formatNames(fs []mediav1.Format) string {
	names := make([]string, len(fs))
	for i, f := range fs {
		names[i] = strings.TrimPrefix(f.String(), "FORMAT_")
	}
	return strings.Join(names, ", ")
}
