# mycroblog/plugin

Official plugins of [mycroblog/my](https://github.com/mycroblog/my). See [Plugins](https://mycroblog.dev/next/plugins/overview/) for what a plugin is and how the server connects to one.

| Plugin | Kind | Implements |
|---|---|---|
| [my-media-imagemagick](plugins/my-media-imagemagick) | `media_image` | `MediaService.Process` with ImageMagick |
| [my-media-ffmpeg](plugins/my-media-ffmpeg) | `media_video`, `media_audio` | `MediaService.Remux` with ffmpeg |

Each plugin's README says how to run it and what it does. Every plugin also serves the probe kind, so `my plugin test --kind probe <address>` checks it before anything else.

## Layout

```
.
├── go.mod            # github.com/mycroblog/plugin: the code the plugins share
├── buf.gen.yaml      # generates gen/ from the .proto files of mycroblog/my at a pinned ref
├── gen/              # generated code of the plugin protocol (do not edit)
├── internal/
│   ├── server/       # listener (unix://, http://, https://), HTTP/2, mutual TLS, bearer token, shutdown
│   ├── probe/        # the probe kind
│   └── media/        # the shared half of the media kinds
├── plugins/          # one Go module per plugin, each with its own dependencies, Dockerfile, and plugin.yaml
├── scripts/          # lint, vet, tidy, generate
└── tools/            # the CI's tools and buf, kept in a module of their own so their dependencies stay out of the root
```

The root module stays small on purpose: a dependency one plugin needs goes in that plugin's `go.mod`, not the root's.
Each plugin's `go.mod` requires the root module and replaces it with `../..`, so a plugin always builds with the root
module of the same commit, and a change to both lands in one pull request. Every module builds on its own
(`GOWORK=off`); the repository has no `go.work`, though you may make one of your own (`go work init . ./plugins/*`), which
git ignores.

## Development

Go is pinned in `.tool-versions`.

```sh
GOWORK=off go test ./...                                # the root module
(cd plugins/my-media-ffmpeg && GOWORK=off go test ./...) # a plugin; its integration tests need its tools, or its image
./scripts/lint.sh                                       # everything CI checks statically
./scripts/tidy.sh                                       # tidy every module, after changing a dependency
```

### Static checks

```sh
./scripts/vet.sh
```

It runs `go vet` and [depcheck](tools/depcheck) over every module. depcheck keeps the imports between the packages of
this repository in their direction, because CI decides what to test from the import graph:

- the root module does not import `plugins/`, and a plugin does not import another plugin;
- `internal/server`, the transport, imports no protocol package;
- `internal/<kind>` imports, of `gen/`, only its own kind, and no other `internal/` package but the shared ones;
- `gen/` imports only `gen/`, and nothing outside `tools/` imports `tools/`;
- an `internal/` package is either of a protocol kind or listed as shared in depcheck, named by what it does.

### Regenerating the protocol code

```sh
./scripts/generate.sh
```

To follow the protocol, bump `ref` in `buf.gen.yaml` and regenerate: a release tag, or a commit of mycroblog/my's main
when a plugin needs a change that is not released yet. CI runs `my plugin test` of the same ref: for a tag, the `my` the
release attaches (from v0.1.0-rc.12 on), after checking its checksum and provenance; for a commit, built from it.

### Running a plugin

```sh
go run ./plugins/my-media-imagemagick -address http://127.0.0.1:8080
```

Flags: `-address` (`unix:///path`, `http://host:port`, `https://host:port`), `-tls-cert-file`, `-tls-key-file`, `-tls-client-ca-file` (mutual TLS), `-token-file` (bearer token; unix:// and https:// only), `-shutdown-timeout`.

### Container images and integration tests

Each plugin's Dockerfile has three stages: `deps`, what the plugin runs with; `test`, `deps` with Go; and `runtime`,
`deps` with the plugin, the default. The integration tests run in `test`, so they see the packages and the configuration
production runs with, and fail rather than skip when a tool is missing (`MY_PLUGIN_REQUIRE_TOOLS`). Build from the
repository root:

```sh
docker run --rm "$(docker build -q --target test -f plugins/my-media-imagemagick/Dockerfile .)"
docker build -f plugins/my-media-imagemagick/Dockerfile -t my-media-imagemagick .
docker run --rm -p 8080:8080 my-media-imagemagick
```

### Contract tests

`plugins/<name>/plugin.yaml` declares the kinds a plugin serves, and the cases of the contract that fail because of
mycroblog/my, each with its issue there. [contract](tools/contract) runs `my plugin test`, of the version
`buf.gen.yaml` pins, against the plugin's image for probe and each kind, over a Unix socket, over a Unix socket with gRPC,
and over plain TCP where the kind allows it. It fails on a failing case plugin.yaml does not list, and on a listed one
that passes or no longer runs.

```sh
gh release download v0.1.0-rc.12 -R mycroblog/my -p 'my_*_linux_amd64.tar.gz' -O - | tar -xz my  # or build it
GOWORK=off go run -C tools ./cmd/contract -root .. -plugin my-media-imagemagick -image my-media-imagemagick -my ./my
```

## Adding a plugin

1. Create `plugins/my-<kind>-<implementation>/` with a `go.mod` whose module path is
   `github.com/mycroblog/plugin/plugins/my-<kind>-<implementation>`, which requires `github.com/mycroblog/plugin` and
   replaces it with `../..`, like the existing plugins.
2. Write `main.go`, a `Dockerfile` with the stages `deps`, `test`, and `runtime`, and a `plugin.yaml`.

CI and Dependabot pick the plugin up by themselves, and CI tests it alone: `./scripts/lint.sh` says what is missing.

## Releases

Each plugin is released on its own, as its container image. Its `go.mod` replaces the root module with `../..`, so it
builds from a checkout of this repository, not with `go install …@version`.

## License

[MIT](LICENSE). The protocol definitions in `gen/` are generated from mycroblog/my, also under the MIT License.
