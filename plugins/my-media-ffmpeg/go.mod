module github.com/mycroblog/plugin/plugins/my-media-ffmpeg

go 1.27.4

require github.com/mycroblog/plugin v0.0.0-00010101000000-000000000000

require (
	connectrpc.com/connect v1.21.1 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/mycroblog/plugin => ../..
