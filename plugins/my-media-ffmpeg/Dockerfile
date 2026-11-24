# syntax=docker/dockerfile:1
# Build from the repository root: docker build -f plugins/my-media-ffmpeg/Dockerfile .
#
#   deps     what the plugin runs with
#   test     deps with Go, for the integration tests: docker run --rm $(docker build -q --target test ...)
#   runtime  deps with the plugin (the default target)
#
# test and runtime start from the same deps, so the tests run with the packages (ffmpeg) and the
# configuration production runs with.
ARG GO_VERSION=1.27.4

FROM golang:${GO_VERSION}-trixie AS go

FROM debian:trixie-slim AS deps
RUN apt-get update \
	&& apt-get install -y --no-install-recommends ffmpeg \
	&& rm -rf /var/lib/apt/lists/*

FROM deps AS test
RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates gcc libc6-dev \
	&& rm -rf /var/lib/apt/lists/*
COPY --from=go /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH GOTOOLCHAIN=local MY_PLUGIN_REQUIRE_TOOLS=1
WORKDIR /src
COPY go.mod go.sum ./
COPY plugins/my-media-ffmpeg/go.mod plugins/my-media-ffmpeg/go.sum* ./plugins/my-media-ffmpeg/
RUN go work init . ./plugins/my-media-ffmpeg && go mod download
COPY gen ./gen
COPY internal ./internal
COPY plugins/my-media-ffmpeg ./plugins/my-media-ffmpeg
CMD ["go", "test", "-race", "-count=1", "./plugins/my-media-ffmpeg/..."]

FROM test AS build
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/my-media-ffmpeg ./plugins/my-media-ffmpeg

FROM deps AS runtime
COPY --from=build /out/my-media-ffmpeg /usr/local/bin/my-media-ffmpeg
USER nobody
EXPOSE 8080
ENTRYPOINT ["my-media-ffmpeg"]
CMD ["-address", "http://:8080"]
