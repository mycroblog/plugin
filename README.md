# mycroblog/plugin

Official plugins of [mycroblog/my](https://github.com/mycroblog/my). See [Plugins](https://mycroblog.dev/next/plugins/overview/) for what a plugin is and how the server connects to one.

| Plugin | Kind | Implements |
|---|---|---|
| [my-media-imagemagick](plugins/my-media-imagemagick) | `media_image` | `MediaService.Process` with ImageMagick |
| [my-media-ffmpeg](plugins/my-media-ffmpeg) | `media_video`, `media_audio` | `MediaService.Remux` with ffmpeg |

Every plugin also serves the probe kind, so `my plugin test --kind probe <address>` checks it before anything else.

## Layout

```
.
├── go.mod            # github.com/mycroblog/plugin: the code the plugins share
├── go.work           # ties the root and the plugin modules together for development
├── buf.gen.yaml      # generates gen/ from the .proto files of mycroblog/my at a pinned tag
├── gen/              # generated code of the plugin protocol (do not edit)
├── internal/
│   ├── server/       # listener (unix://, http://, https://), HTTP/2, mutual TLS, bearer token, shutdown
│   ├── probe/        # the probe kind
│   └── media/        # the shared half of the media kinds
├── plugins/          # one Go module per plugin, each with its own dependencies, Dockerfile, and releases
└── tools/            # buf, kept in a module of its own so its dependencies stay out of the root
```

The root module stays small on purpose: a dependency one plugin needs goes in that plugin's `go.mod`, not the root's.

## Development

Go is pinned in `.tool-versions`.

```sh
go build ./... ./plugins/my-media-imagemagick ./plugins/my-media-ffmpeg
go test ./... ./plugins/my-media-imagemagick ./plugins/my-media-ffmpeg
```

### Regenerating the protocol code

```sh
./scripts/generate.sh
```

To follow a new protocol release, bump `tag` in `buf.gen.yaml` and regenerate.

### Running a plugin

```sh
go run ./plugins/my-media-imagemagick -address http://127.0.0.1:8080
```

Flags: `-address` (`unix:///path`, `http://host:port`, `https://host:port`), `-tls-cert-file`, `-tls-key-file`, `-tls-client-ca-file` (mutual TLS), `-token-file` (bearer token; unix:// and https:// only), `-shutdown-timeout`.

### Container images

Build from the repository root:

```sh
docker build -f plugins/my-media-imagemagick/Dockerfile -t my-media-imagemagick .
docker run --rm -p 8080:8080 my-media-imagemagick
```

## Adding a plugin

1. Create `plugins/my-<kind>-<implementation>/` with a `go.mod` whose module path is `github.com/mycroblog/plugin/plugins/my-<kind>-<implementation>`.
2. Add it to `go.work` (`go work use ./plugins/...`).
3. Write `main.go` and a `Dockerfile` like the existing plugins.

## Releases

Each module is versioned on its own. The root takes tags like `v0.1.0`, a plugin takes tags prefixed with its path, like `plugins/my-media-ffmpeg/v0.1.0`.

## License

[MIT](LICENSE). The protocol definitions in `gen/` are generated from mycroblog/my, also under the MIT License.
