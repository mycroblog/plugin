// Command my-media-ffmpeg is a media plugin of mycroblog/my that implements MediaService.Remux (video and audio) with ffmpeg.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/mycroblog/plugin/internal/media"
	"github.com/mycroblog/plugin/internal/probe"
	"github.com/mycroblog/plugin/internal/server"
)

func main() {
	var cfg server.Config
	cfg.RegisterFlags(flag.CommandLine)
	flag.Parse()

	mux := http.NewServeMux()
	mux.Handle(probe.NewHandler())
	mux.Handle(media.NewHandler())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := server.Run(ctx, cfg, mux); err != nil {
		fmt.Fprintln(os.Stderr, "my-media-ffmpeg:", err)
		os.Exit(1)
	}
}
