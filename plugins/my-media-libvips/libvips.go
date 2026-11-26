package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	mediav1 "github.com/mycroblog/plugin/gen/my/plugin/media/v1"
	"github.com/mycroblog/plugin/internal/media"
)

// Converter implements media.ImageProcessor with libvips, each image in a worker (worker.go).
type Converter struct {
	// Worker is the command of a worker: this binary, which the environment makes one.
	Worker string
	// WebPQuality and JPEGQuality are the qualities of the lossy outputs, from 1 to 100.
	WebPQuality int
	JPEGQuality int
	// Threads is the size of the pool of threads of each worker; 0 leaves libvips its default, one per CPU.
	Threads int

	// inputFormats are the formats beyond the built-in ones that this libvips decodes, which check finds.
	inputFormats []mediav1.Format
}

// InputFormats implements media.ImageProcessor: AVIF and HEIC when check decoded them with this libvips. An APNG is
// not declared: libvips reads its default image, so the server reads it as a still PNG.
func (c *Converter) InputFormats() []mediav1.Format {
	return slices.Clone(c.inputFormats)
}

// builtinFormats are the formats that the plugin always accepts; an APNG is a PNG to it.
var builtinFormats = map[mediav1.Format]bool{
	mediav1.Format_FORMAT_JPEG: true, mediav1.Format_FORMAT_PNG: true, mediav1.Format_FORMAT_APNG: true,
	mediav1.Format_FORMAT_GIF: true, mediav1.Format_FORMAT_WEBP: true,
}

// Process implements media.ImageProcessor.
func (c *Converter) Process(ctx context.Context, in media.Image) (media.ImageResult, error) {
	format, err := media.SniffImageFile(in.Path)
	if err != nil {
		return media.ImageResult{}, err
	}
	if !builtinFormats[format] && !slices.Contains(c.inputFormats, format) {
		return media.ImageResult{Outcome: mediav1.Outcome_OUTCOME_UNSUPPORTED_FORMAT}, nil
	}
	head, err := proto.Marshal(in.Head)
	if err != nil {
		return media.ImageResult{}, err
	}
	a, err := c.run(ctx, in.Dir, job{
		Path: in.Path, Dir: in.Dir, Format: format, Head: head,
		WebPQuality: c.WebPQuality, JPEGQuality: c.JPEGQuality, Threads: c.Threads,
	})
	if err != nil {
		return media.ImageResult{}, err
	}
	if a.Error != "" {
		return media.ImageResult{}, errors.New(a.Error)
	}
	return a.Result, nil
}

// check refuses to start with a setting that would fail every call, and finds the formats beyond the built-in ones
// that this libvips decodes.
func (c *Converter) check(ctx context.Context) (version string, err error) {
	var errs []error
	for name, q := range map[string]int{"-webp-quality": c.WebPQuality, "-jpeg-quality": c.JPEGQuality} {
		if q < 1 || q > 100 {
			errs = append(errs, fmt.Errorf("%s is %d, not from 1 to 100", name, q))
		}
	}
	dir, err := os.MkdirTemp("", "my-media-libvips-")
	if err != nil {
		return "", errors.Join(append(errs, err)...)
	}
	defer os.RemoveAll(dir)
	a, err := c.run(ctx, dir, job{Check: true, Dir: dir, Threads: c.Threads})
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("libvips: %w", err))
	case a.Error != "":
		errs = append(errs, fmt.Errorf("libvips: %s", a.Error))
	case len(a.Missing) > 0:
		errs = append(errs, fmt.Errorf("libvips %s lacks %s", a.Version, strings.Join(a.Missing, ", ")))
	}
	c.inputFormats = a.Formats
	return a.Version, errors.Join(errs...)
}

// run runs a job in a worker. The worker writes its temporary files in dir, the directory of the call, which goes
// with it.
func (c *Converter) run(ctx context.Context, dir string, j job) (answer, error) {
	in, err := json.Marshal(j)
	if err != nil {
		return answer{}, err
	}
	cmd := exec.CommandContext(ctx, c.Worker)
	cmd.Env = append(os.Environ(), workerEnv+"=1", "TMPDIR="+dir)
	var stdout, stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(in), &stdout, &stderr
	err = cmd.Run()
	if ctx.Err() != nil {
		return answer{}, ctx.Err()
	}
	if err != nil {
		return answer{}, fmt.Errorf("worker: %w: %s", err, lastLine(stderr.String()))
	}
	var a answer
	if err := json.Unmarshal(stdout.Bytes(), &a); err != nil {
		return answer{}, fmt.Errorf("worker: its answer: %w", err)
	}
	return a, nil
}

// lastLine returns the last line of what a worker wrote on its standard error, which names why it failed.
func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return s
}
