package media

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	mediav1 "github.com/mycroblog/plugin/gen/my/plugin/media/v1"
	"github.com/mycroblog/plugin/gen/my/plugin/media/v1/mediav1connect"
)

// images copies the input to every still output, or answers outcome or err when set.
type images struct {
	outcome mediav1.Outcome
	err     error
	dir     string
}

func (p *images) Process(_ context.Context, in Image) (ImageResult, error) {
	p.dir = in.Dir
	if p.err != nil {
		return ImageResult{}, p.err
	}
	if p.outcome != mediav1.Outcome_OUTCOME_UNSPECIFIED {
		return ImageResult{Outcome: p.outcome}, nil
	}
	res := ImageResult{Outcome: mediav1.Outcome_OUTCOME_OK, Width: 3, Height: 2, Blurhash: "LEHV6nWB2yk8"}
	for _, o := range []mediav1.Output{mediav1.Output_OUTPUT_STILL_SMALL, mediav1.Output_OUTPUT_STILL_MEDIUM, mediav1.Output_OUTPUT_STILL_LARGE} {
		res.Outputs = append(res.Outputs, Output{Output: o, Format: mediav1.Format_FORMAT_WEBP, Width: 3, Height: 2, Path: in.Path})
	}
	return res, nil
}

func newClient(t *testing.T, o Options) mediav1connect.MediaServiceClient {
	t.Helper()
	o.Logger = slog.New(slog.DiscardHandler)
	mux := http.NewServeMux()
	mux.Handle(NewHandler(o))
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return mediav1connect.NewMediaServiceClient(srv.Client(), srv.URL)
}

type received struct {
	head   *mediav1.ProcessResult
	parts  []*mediav1.Part
	bodies [][]byte
}

// process sends head and data in chunks of chunk bytes, and gathers the answer.
func process(t *testing.T, c mediav1connect.MediaServiceClient, head *mediav1.ProcessHead, data []byte, chunk int) (received, error) {
	t.Helper()
	stream := c.Process(context.Background())
	if err := stream.Send(&mediav1.ProcessRequest{Message: &mediav1.ProcessRequest_Head{Head: head}}); err != nil {
		return received{}, err
	}
	for len(data) > 0 {
		n := min(chunk, len(data))
		if err := stream.Send(&mediav1.ProcessRequest{Message: &mediav1.ProcessRequest_Chunk{Chunk: data[:n]}}); err != nil {
			break // the plugin answered early; Receive reports it
		}
		data = data[n:]
	}
	if err := stream.CloseRequest(); err != nil {
		return received{}, err
	}
	var r received
	for {
		res, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			return r, stream.CloseResponse()
		}
		if err != nil {
			return r, err
		}
		switch m := res.GetMessage().(type) {
		case *mediav1.ProcessResponse_Head:
			r.head = m.Head
		case *mediav1.ProcessResponse_Part:
			r.parts = append(r.parts, m.Part)
			r.bodies = append(r.bodies, nil)
		case *mediav1.ProcessResponse_Chunk:
			r.bodies[len(r.bodies)-1] = append(r.bodies[len(r.bodies)-1], m.Chunk...)
		}
	}
}

func head(size int64) *mediav1.ProcessHead {
	return &mediav1.ProcessHead{Size: size, Limits: &mediav1.Limits{MaxBytes: 16 << 20}}
}

func TestProcess(t *testing.T) {
	p := &images{}
	c := newClient(t, Options{Images: p})
	data := bytes.Repeat([]byte("0123456789abcdef"), (ChunkSize*5/2)/16) // two and a half chunks

	r, err := process(t, c, head(int64(len(data))), data, ChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	if r.head.GetOutcome() != mediav1.Outcome_OUTCOME_OK || r.head.GetWidth() != 3 || r.head.GetBlurhash() == "" {
		t.Fatalf("head = %v", r.head)
	}
	if len(r.parts) != 3 {
		t.Fatalf("%d parts, want 3", len(r.parts))
	}
	for i, part := range r.parts {
		if part.GetSize() != int64(len(data)) || !bytes.Equal(r.bodies[i], data) {
			t.Errorf("part %v: size %d, %d bytes received, want %d", part.GetOutput(), part.GetSize(), len(r.bodies[i]), len(data))
		}
	}
	if _, err := os.Stat(p.dir); !os.IsNotExist(err) {
		t.Errorf("the directory of the call is left: %v", err)
	}
}

func TestProcessOutcomes(t *testing.T) {
	tests := []struct {
		name string
		p    *images
		head *mediav1.ProcessHead
		want mediav1.Outcome
	}{
		{"refused by the implementation", &images{outcome: mediav1.Outcome_OUTCOME_CORRUPT}, head(4), mediav1.Outcome_OUTCOME_CORRUPT},
		{"failure of the implementation", &images{err: errors.New("magick crashed")}, head(4), mediav1.Outcome_OUTCOME_TEMPORARY},
		{"larger than the limit", &images{}, &mediav1.ProcessHead{Size: 4, Limits: &mediav1.Limits{MaxBytes: 3}}, mediav1.Outcome_OUTCOME_TOO_LARGE},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := process(t, newClient(t, Options{Images: tt.p}), tt.head, []byte("data"), ChunkSize)
			if err != nil {
				t.Fatal(err)
			}
			if r.head.GetOutcome() != tt.want || len(r.parts) != 0 {
				t.Errorf("outcome %v with %d parts, want %v and none", r.head.GetOutcome(), len(r.parts), tt.want)
			}
		})
	}
}

func TestProcessRefusesBadStreams(t *testing.T) {
	c := newClient(t, Options{Images: &images{}})
	for _, tt := range []struct {
		name string
		size int64
		data []byte
	}{
		{"fewer bytes than the head says", 10, []byte("data")},
		{"more bytes than the head says", 2, []byte("data")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := process(t, c, head(tt.size), tt.data, ChunkSize)
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Errorf("err = %v, want InvalidArgument", err)
			}
		})
	}
}

func TestUnimplemented(t *testing.T) {
	_, err := process(t, newClient(t, Options{}), head(4), []byte("data"), ChunkSize)
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("err = %v, want Unimplemented", err)
	}
}

func TestTempDir(t *testing.T) {
	tmp := t.TempDir()
	p := &images{}
	if _, err := process(t, newClient(t, Options{Images: p, TempDir: tmp}), head(4), []byte("data"), ChunkSize); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(p.dir) != tmp {
		t.Errorf("the call worked in %s, not under %s", p.dir, tmp)
	}
}

// av returns its input as the body, and a cover.
type av struct{}

func (av) Remux(_ context.Context, in AV) (AVResult, error) {
	cover := filepath.Join(in.Dir, "cover.png")
	if err := os.WriteFile(cover, []byte("png"), 0o600); err != nil {
		return AVResult{}, err
	}
	return AVResult{Outcome: mediav1.Outcome_OUTCOME_OK, Outputs: []Output{
		{Output: mediav1.Output_OUTPUT_AV_BODY, Format: mediav1.Format_FORMAT_MP3, Path: in.Path},
		{Output: mediav1.Output_OUTPUT_AV_COVER, Format: mediav1.Format_FORMAT_PNG, Width: 1, Height: 1, Path: cover},
	}}, nil
}

func TestRemux(t *testing.T) {
	c := newClient(t, Options{AV: av{}})
	stream := c.Remux(context.Background())
	data := []byte("ID3 and frames")
	for _, m := range []*mediav1.RemuxRequest{
		{Message: &mediav1.RemuxRequest_Head{Head: &mediav1.RemuxHead{Size: int64(len(data)), Kind: mediav1.AVKind_AV_KIND_AUDIO, Limits: &mediav1.AVLimits{MaxBytes: 100 << 20}}}},
		{Message: &mediav1.RemuxRequest_Chunk{Chunk: data}},
	} {
		if err := stream.Send(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := stream.CloseRequest(); err != nil {
		t.Fatal(err)
	}
	var outcome mediav1.Outcome
	bodies := map[mediav1.Output][]byte{}
	var last mediav1.Output
	for {
		res, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch m := res.GetMessage().(type) {
		case *mediav1.RemuxResponse_Head:
			outcome = m.Head.GetOutcome()
		case *mediav1.RemuxResponse_Part:
			last = m.Part.GetOutput()
		case *mediav1.RemuxResponse_Chunk:
			bodies[last] = append(bodies[last], m.Chunk...)
		}
	}
	if outcome != mediav1.Outcome_OUTCOME_OK {
		t.Fatalf("outcome = %v", outcome)
	}
	if !bytes.Equal(bodies[mediav1.Output_OUTPUT_AV_BODY], data) || string(bodies[mediav1.Output_OUTPUT_AV_COVER]) != "png" {
		t.Errorf("outputs = %q", bodies)
	}
}
