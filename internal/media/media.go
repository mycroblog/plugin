// Package media is the shared half of the media kinds (my.plugin.media.v1): media_image, media_video, and media_audio.
//
// It is meant to own the stream framing of MediaService (a head, then chunks of 1 MiB at most, then one part per
// output) so that an implementation in plugins/ only converts bytes.
package media

import (
	"net/http"

	"connectrpc.com/connect"

	"github.com/mycroblog/plugin/gen/my/plugin/media/v1/mediav1connect"
)

// TODO: define the interfaces implementations satisfy (one for Process, one for Remux), and a service that reads the
// request stream, calls them, and writes the response stream. Until then every call answers Unimplemented.

// NewHandler returns the path and the handler of MediaService.
func NewHandler(opts ...connect.HandlerOption) (string, http.Handler) {
	return mediav1connect.NewMediaServiceHandler(mediav1connect.UnimplementedMediaServiceHandler{}, opts...)
}
