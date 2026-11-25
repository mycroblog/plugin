// Package media is the shared half of the media kinds (my.plugin.media.v1): media_image, media_video, and media_audio.
//
// It owns the stream framing of MediaService, so that an implementation in plugins/ only converts files: the service
// writes the input to a file in a temporary directory of the call, hands the implementation the file and the head, and
// streams the files of its outputs back in parts and chunks. The directory is removed when the call ends.
package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"connectrpc.com/connect"

	mediav1 "github.com/mycroblog/plugin/gen/my/plugin/media/v1"
	"github.com/mycroblog/plugin/gen/my/plugin/media/v1/mediav1connect"
)

const (
	// ChunkSize is the largest chunk the protocol allows, in both directions.
	ChunkSize = 1 << 20
	// maxMessageBytes is the largest message the protocol allows.
	maxMessageBytes = 2 << 20
)

// Image is the input of ImageProcessor.Process.
type Image struct {
	Head *mediav1.ProcessHead
	// Path is the file holding the image, Head.Size bytes long.
	Path string
	// Dir is a directory of the call, empty, for the outputs and any other file. It is removed when the call ends.
	Dir string
}

// ImageResult is what ImageProcessor.Process makes.
type ImageResult struct {
	// Outcome is OUTCOME_OK with the outputs, or why the image is refused.
	Outcome  mediav1.Outcome
	Width    int32
	Height   int32
	Blurhash string
	Outputs  []Output
}

// ImageProcessor re-encodes an image into its variants.
type ImageProcessor interface {
	// Process answers an outcome for an image it refuses, and an error for a failure of its own, which the service
	// answers as OUTCOME_TEMPORARY.
	Process(ctx context.Context, in Image) (ImageResult, error)
	// InputFormats returns the formats that Process decodes beyond JPEG, PNG, GIF, and WebP, which it always decodes:
	// some of FORMAT_AVIF, FORMAT_HEIC, and FORMAT_APNG. Describe answers them; the server sends such files only once
	// they are declared, and reads an APNG as a still PNG until FORMAT_APNG is.
	InputFormats() []mediav1.Format
}

// AV is the input of AVRemuxer.Remux.
type AV struct {
	Head *mediav1.RemuxHead
	// Path is the file holding the video or audio file, Head.Size bytes long.
	Path string
	// Dir is a directory of the call, empty, for the outputs and any other file. It is removed when the call ends.
	Dir string
}

// AVResult is what AVRemuxer.Remux makes.
type AVResult struct {
	// Outcome is OUTCOME_OK with the outputs, or why the file is refused.
	Outcome mediav1.Outcome
	Outputs []Output
}

// AVRemuxer writes a video or audio file again without its metadata.
type AVRemuxer interface {
	// Remux answers an outcome for a file it refuses, and an error for a failure of its own, which the service answers
	// as OUTCOME_TEMPORARY.
	Remux(ctx context.Context, in AV) (AVResult, error)
}

// Output is one output, held in a file.
type Output struct {
	Output mediav1.Output
	Format mediav1.Format
	// Width and Height are those of an image output; zero for OUTPUT_AV_BODY.
	Width  int32
	Height int32
	// Path is the file holding the output, usually in the Dir of the call.
	Path string
}

// Options configure the service.
type Options struct {
	// Images serves Process and Describe, and AV serves Remux. A nil one answers Unimplemented.
	Images ImageProcessor
	AV     AVRemuxer
	// Concurrency bounds the calls processed at once; further calls wait. Zero or less is 1.
	Concurrency int
	// TempDir is where the directories of the calls go; empty is os.TempDir.
	TempDir string
	// Logger logs the outcome and the duration of each call, never its content; nil is slog.Default.
	Logger *slog.Logger
}

// NewHandler returns the path and the handler of MediaService.
func NewHandler(o Options, opts ...connect.HandlerOption) (string, http.Handler) {
	if o.Concurrency <= 0 {
		o.Concurrency = 1
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	s := &service{o: o, slots: make(chan struct{}, o.Concurrency)}
	opts = append([]connect.HandlerOption{connect.WithReadMaxBytes(maxMessageBytes), connect.WithSendMaxBytes(maxMessageBytes)}, opts...)
	return mediav1connect.NewMediaServiceHandler(s, opts...)
}

type service struct {
	o     Options
	slots chan struct{}
}

// Describe answers the input formats of Images. It takes no slot: the server calls it at start and every 5 minutes, and
// a busy plugin must still answer it.
func (s *service) Describe(context.Context, *connect.Request[mediav1.DescribeRequest]) (*connect.Response[mediav1.DescribeResponse], error) {
	if s.o.Images == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("this plugin does not process images"))
	}
	return connect.NewResponse(&mediav1.DescribeResponse{InputFormats: s.o.Images.InputFormats()}), nil
}

func (s *service) Process(ctx context.Context, stream *connect.BidiStream[mediav1.ProcessRequest, mediav1.ProcessResponse]) error {
	if s.o.Images == nil {
		return connect.NewError(connect.CodeUnimplemented, errors.New("this plugin does not process images"))
	}
	first, err := stream.Receive()
	if err != nil {
		return protocolError(err)
	}
	head := first.GetHead()
	if head == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("the first message is not a head"))
	}
	limits := head.GetLimits()
	return s.call(ctx, "process", head.GetSize(), limits.GetMaxBytes(),
		func() ([]byte, error) {
			req, err := stream.Receive()
			if err != nil {
				return nil, err
			}
			if req.GetHead() != nil {
				return nil, errors.New("a second head")
			}
			return req.GetChunk(), nil
		},
		func(ctx context.Context, path, dir string) (mediav1.Outcome, []Output, func() error, error) {
			res, err := s.o.Images.Process(ctx, Image{Head: head, Path: path, Dir: dir})
			sendHead := func() error {
				return stream.Send(&mediav1.ProcessResponse{Message: &mediav1.ProcessResponse_Head{Head: &mediav1.ProcessResult{
					Outcome: res.Outcome, Width: res.Width, Height: res.Height, Blurhash: res.Blurhash,
				}}})
			}
			return res.Outcome, res.Outputs, sendHead, err
		},
		func(outcome mediav1.Outcome) error {
			return stream.Send(&mediav1.ProcessResponse{Message: &mediav1.ProcessResponse_Head{Head: &mediav1.ProcessResult{Outcome: outcome}}})
		},
		func(p *mediav1.Part) error {
			return stream.Send(&mediav1.ProcessResponse{Message: &mediav1.ProcessResponse_Part{Part: p}})
		},
		func(b []byte) error {
			return stream.Send(&mediav1.ProcessResponse{Message: &mediav1.ProcessResponse_Chunk{Chunk: b}})
		},
	)
}

func (s *service) Remux(ctx context.Context, stream *connect.BidiStream[mediav1.RemuxRequest, mediav1.RemuxResponse]) error {
	if s.o.AV == nil {
		return connect.NewError(connect.CodeUnimplemented, errors.New("this plugin does not remux video or audio"))
	}
	first, err := stream.Receive()
	if err != nil {
		return protocolError(err)
	}
	head := first.GetHead()
	if head == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("the first message is not a head"))
	}
	return s.call(ctx, "remux", head.GetSize(), head.GetLimits().GetMaxBytes(),
		func() ([]byte, error) {
			req, err := stream.Receive()
			if err != nil {
				return nil, err
			}
			if req.GetHead() != nil {
				return nil, errors.New("a second head")
			}
			return req.GetChunk(), nil
		},
		func(ctx context.Context, path, dir string) (mediav1.Outcome, []Output, func() error, error) {
			res, err := s.o.AV.Remux(ctx, AV{Head: head, Path: path, Dir: dir})
			sendHead := func() error {
				return stream.Send(&mediav1.RemuxResponse{Message: &mediav1.RemuxResponse_Head{Head: &mediav1.RemuxResult{Outcome: res.Outcome}}})
			}
			return res.Outcome, res.Outputs, sendHead, err
		},
		func(outcome mediav1.Outcome) error {
			return stream.Send(&mediav1.RemuxResponse{Message: &mediav1.RemuxResponse_Head{Head: &mediav1.RemuxResult{Outcome: outcome}}})
		},
		func(p *mediav1.Part) error {
			return stream.Send(&mediav1.RemuxResponse{Message: &mediav1.RemuxResponse_Part{Part: p}})
		},
		func(b []byte) error {
			return stream.Send(&mediav1.RemuxResponse{Message: &mediav1.RemuxResponse_Chunk{Chunk: b}})
		},
	)
}

// call runs one call after its head: it waits for a slot, writes the input to a file, runs the implementation, and
// streams the outputs. The functions are the halves of the call that differ between Process and Remux.
func (s *service) call(
	ctx context.Context,
	rpc string,
	size, maxBytes int64,
	receive func() ([]byte, error),
	run func(ctx context.Context, path, dir string) (mediav1.Outcome, []Output, func() error, error),
	sendOutcome func(mediav1.Outcome) error,
	sendPart func(*mediav1.Part) error,
	sendChunk func([]byte) error,
) (err error) {
	start := time.Now()
	outcome := mediav1.Outcome_OUTCOME_UNSPECIFIED
	defer func() {
		attrs := []any{slog.String("rpc", rpc), slog.Int64("size", size), slog.Duration("duration", time.Since(start)), slog.String("outcome", outcome.String())}
		if err != nil {
			s.o.Logger.Warn("media call failed", append(attrs, slog.Any("error", err))...)
		} else {
			s.o.Logger.Info("media call", attrs...)
		}
	}()

	if size < 0 {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("a size of %d", size))
	}
	if maxBytes > 0 && size > maxBytes {
		outcome = mediav1.Outcome_OUTCOME_TOO_LARGE
		return sendOutcome(outcome)
	}

	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return connect.NewError(connect.CodeCanceled, ctx.Err())
	}

	dir, err := os.MkdirTemp(s.o.TempDir, "my-media-*")
	if err != nil {
		outcome = mediav1.Outcome_OUTCOME_TEMPORARY
		return errors.Join(err, sendOutcome(outcome))
	}
	defer os.RemoveAll(dir)

	path, err := writeInput(dir, size, receive)
	if err != nil {
		return err
	}

	outcome, outputs, sendHead, runErr := run(ctx, path, dir)
	if runErr != nil {
		if ctx.Err() != nil {
			return connect.NewError(connect.CodeCanceled, ctx.Err())
		}
		outcome = mediav1.Outcome_OUTCOME_TEMPORARY
		s.o.Logger.Warn("media conversion failed", slog.String("rpc", rpc), slog.Any("error", runErr))
		return sendOutcome(outcome)
	}
	if outcome != mediav1.Outcome_OUTCOME_OK {
		return sendOutcome(outcome)
	}
	if err := sendHead(); err != nil {
		return err
	}
	for _, o := range outputs {
		if err := sendOutput(o, sendPart, sendChunk); err != nil {
			return err
		}
	}
	return nil
}

// writeInput writes the chunks to a file of dir and checks that they add up to size.
func writeInput(dir string, size int64, receive func() ([]byte, error)) (string, error) {
	f, err := os.CreateTemp(dir, "input-*")
	if err != nil {
		return "", err
	}
	defer f.Close()
	var n int64
	for {
		chunk, err := receive()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", protocolError(err)
		}
		if len(chunk) > ChunkSize {
			return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("a chunk of %d bytes", len(chunk)))
		}
		n += int64(len(chunk))
		if n > size {
			return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("more than the %d bytes of the head", size))
		}
		if _, err := f.Write(chunk); err != nil {
			return "", err
		}
	}
	if n != size {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%d bytes, not the %d of the head", n, size))
	}
	return f.Name(), f.Close()
}

func sendOutput(o Output, sendPart func(*mediav1.Part) error, sendChunk func([]byte) error) error {
	f, err := os.Open(o.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if err := sendPart(&mediav1.Part{Output: o.Output, Format: o.Format, Width: o.Width, Height: o.Height, Size: fi.Size()}); err != nil {
		return err
	}
	buf := make([]byte, ChunkSize)
	for {
		n, err := io.ReadFull(f, buf)
		if n > 0 {
			if err := sendChunk(buf[:n]); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// protocolError wraps an error of the request stream, keeping its code when it has one.
func protocolError(err error) error {
	if errors.Is(err, io.EOF) {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("the request ended early"))
	}
	if ce := new(connect.Error); errors.As(err, &ce) {
		return ce
	}
	return connect.NewError(connect.CodeInvalidArgument, err)
}
