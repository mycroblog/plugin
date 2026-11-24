package probe

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	probev1 "github.com/mycroblog/plugin/gen/my/plugin/probe/v1"
	"github.com/mycroblog/plugin/gen/my/plugin/probe/v1/probev1connect"
)

func TestEcho(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(NewHandler())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := probev1connect.NewProbeServiceClient(srv.Client(), srv.URL, connect.WithProtoJSON())
	res, err := client.Echo(context.Background(), connect.NewRequest(&probev1.EchoRequest{Payload: []byte("hi"), Number: 42}))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Msg.GetOutcome(); got != probev1.Outcome_OUTCOME_OK {
		t.Errorf("outcome = %v, want OUTCOME_OK", got)
	}
	if !bytes.Equal(res.Msg.GetPayload(), []byte("hi")) || res.Msg.GetNumber() != 42 {
		t.Errorf("echo = (%q, %d), want (\"hi\", 42)", res.Msg.GetPayload(), res.Msg.GetNumber())
	}
}
