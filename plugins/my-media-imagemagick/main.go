// Command my-media-imagemagick is a media plugin of mycroblog/my that implements MediaService.Process (images) with
// ImageMagick.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

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
	}
	return errors.Join(errs...)
}
