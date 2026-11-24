// Command my-media-imagemagick is a media plugin of mycroblog/my that implements MediaService.Process (images) with ImageMagick.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
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
	flag.Parse()

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
