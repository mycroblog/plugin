module github.com/mycroblog/plugin/plugins/my-media-libvips

go 1.27.4

require (
	github.com/mycroblog/plugin v0.0.0-00010101000000-000000000000
	google.golang.org/protobuf v1.36.12
)

require connectrpc.com/connect v1.21.1 // indirect

replace github.com/mycroblog/plugin => ../..
