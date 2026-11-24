// Package probe implements the probe kind (my.plugin.probe.v1), which every plugin of this repository serves so that
// `my plugin test --kind probe` can check its transport, protocol, and authentication.
package probe

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	probev1 "github.com/mycroblog/plugin/gen/my/plugin/probe/v1"
	"github.com/mycroblog/plugin/gen/my/plugin/probe/v1/probev1connect"
)

// NewHandler returns the path and the handler of ProbeService.
func NewHandler(opts ...connect.HandlerOption) (string, http.Handler) {
	return probev1connect.NewProbeServiceHandler(service{}, opts...)
}

type service struct{}

func (service) Echo(_ context.Context, req *connect.Request[probev1.EchoRequest]) (*connect.Response[probev1.EchoResponse], error) {
	return connect.NewResponse(&probev1.EchoResponse{
		Outcome: probev1.Outcome_OUTCOME_OK,
		Payload: req.Msg.GetPayload(),
		Number:  req.Msg.GetNumber(),
	}), nil
}
