// Command my-media-libvips is a media plugin of mycroblog/my that implements MediaService.Process (images) with
// libvips.
package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	mediav1 "github.com/mycroblog/plugin/gen/my/plugin/media/v1"
	"github.com/mycroblog/plugin/internal/media"
	"github.com/mycroblog/plugin/internal/probe"
	"github.com/mycroblog/plugin/internal/server"
)

// samples holds an image of each format beyond the built-in ones that libvips may decode, through libheif and its
// decoders, which an installation may lack.
//
//go:embed samples
var samples embed.FS

func main() {
	if os.Getenv(workerEnv) == "1" {
		os.Exit(runWorker(os.Stdin, os.Stdout))
	}

	var cfg server.Config
	cfg.RegisterFlags(flag.CommandLine)
	var opts media.Options
	// The server runs at most two image calls at once; each image gets its share of the CPUs.
	flag.IntVar(&opts.Concurrency, "concurrency", 2, "images processed at once, each with its share of the CPUs; further calls wait")
	c := &Converter{}
	flag.IntVar(&c.WebPQuality, "webp-quality", 80, "quality of the WebP outputs, from 1 to 100")
	flag.IntVar(&c.JPEGQuality, "jpeg-quality", 85, "quality of the JPEG outputs, from 1 to 100")
	flag.Parse()

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "my-media-libvips:", err)
		os.Exit(1)
	}
	c.Worker = exe
	// The CPUs shared among the images processed at once.
	c.Threads = max(1, runtime.NumCPU()/max(1, opts.Concurrency))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	version, err := c.check(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "my-media-libvips:", err)
		os.Exit(2)
	}
	opts.Images = c
	fmt.Fprintf(os.Stderr, "my-media-libvips: libvips %s; accepts JPEG, PNG, GIF, WebP%s\n", version, formatNames(c.inputFormats))

	mux := http.NewServeMux()
	mux.Handle(probe.NewHandler())
	mux.Handle(media.NewHandler(opts))

	if err := server.Run(ctx, cfg, mux); err != nil {
		fmt.Fprintln(os.Stderr, "my-media-libvips:", err)
		os.Exit(1)
	}
}

func formatNames(fs []mediav1.Format) string {
	var b strings.Builder
	for _, f := range fs {
		b.WriteString(", " + strings.TrimPrefix(f.String(), "FORMAT_"))
	}
	return b.String()
}
