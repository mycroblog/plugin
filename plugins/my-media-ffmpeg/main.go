// Command my-media-ffmpeg is a media plugin of mycroblog/my that implements MediaService.Remux (video and audio) with
// FFmpeg.
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
	"regexp"
	"runtime"
	"strconv"
	"syscall"

	"github.com/mycroblog/plugin/internal/media"
	"github.com/mycroblog/plugin/internal/probe"
	"github.com/mycroblog/plugin/internal/server"
)

func main() {
	var cfg server.Config
	cfg.RegisterFlags(flag.CommandLine)
	var opts media.Options
	flag.IntVar(&opts.Concurrency, "concurrency", runtime.NumCPU(), "files processed at once; further calls wait")
	r := &Remuxer{}
	flag.StringVar(&r.FFmpeg, "ffmpeg", "ffmpeg", "the ffmpeg command")
	flag.StringVar(&r.FFprobe, "ffprobe", "ffprobe", "the ffprobe command")
	flag.Parse()

	if err := r.check(); err != nil {
		fmt.Fprintln(os.Stderr, "my-media-ffmpeg:", err)
		os.Exit(2)
	}
	opts.AV = r

	mux := http.NewServeMux()
	mux.Handle(probe.NewHandler())
	mux.Handle(media.NewHandler(opts))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := server.Run(ctx, cfg, mux); err != nil {
		fmt.Fprintln(os.Stderr, "my-media-ffmpeg:", err)
		os.Exit(1)
	}
}

var versionRE = regexp.MustCompile(`^ff(?:mpeg|probe) version n?(\d+)\.`)

// check refuses to start with commands that would fail every call: FFmpeg 7 or later, with the encoders of covers.
func (r *Remuxer) check() error {
	var errs []error
	for flagName, cmd := range map[string]string{"-ffmpeg": r.FFmpeg, "-ffprobe": r.FFprobe} {
		out, err := exec.Command(cmd, "-version").Output()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %s: %w", flagName, cmd, err))
			continue
		}
		m := versionRE.FindSubmatch(out)
		if m == nil {
			errs = append(errs, fmt.Errorf("%s: %s does not say its version", flagName, cmd))
			continue
		}
		if major, _ := strconv.Atoi(string(m[1])); major < 7 {
			errs = append(errs, fmt.Errorf("%s: %s is FFmpeg %d, not 7 or later", flagName, cmd, major))
		}
	}
	if len(errs) == 0 {
		out, _ := exec.Command(r.FFmpeg, "-hide_banner", "-encoders").Output()
		for _, enc := range []string{"mjpeg", "png"} {
			if !regexp.MustCompile(`(?m)^ V\S* ` + enc + ` `).Match(out) {
				errs = append(errs, fmt.Errorf("-ffmpeg: %s has no %s encoder, which cover pictures need", r.FFmpeg, enc))
			}
		}
	}
	return errors.Join(errs...)
}
