# syntax=docker/dockerfile:1
# Build from the repository root: docker build -f plugins/my-media-imagemagick/Dockerfile .
#
#   deps     what the plugin runs with
#   test     deps with Go, for the integration tests: docker run --rm $(docker build -q --target test ...)
#   runtime  deps with the plugin (the default target)
#
# test and runtime start from the same deps, so the tests run with the packages and the configuration production runs
# with. The libheif plugins decode AVIF (dav1d) and HEIC (libde265); they are decoders only, as the plugin never writes
# either format.
ARG GO_VERSION=1.27.4

FROM golang:${GO_VERSION}-trixie AS go

# The digest pins the distribution, so that a change of its packages comes as a pull request (Dependabot) that CI tests.
FROM debian:trixie-slim@sha256:8aa5a7bf5a1955bddbe7d74fcdd0b91ab30e74362a8b73af672b0481b35b44a0 AS deps
RUN apt-get update \
	&& apt-get install -y --no-install-recommends imagemagick icc-profiles-free \
		libheif-plugin-dav1d libheif-plugin-libde265 \
	&& rm -rf /var/lib/apt/lists/*
COPY plugins/my-media-imagemagick/policy.xml /etc/ImageMagick-7/policy.xml

FROM deps AS test
RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates gcc libc6-dev \
	&& rm -rf /var/lib/apt/lists/*
COPY --from=go /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH GOTOOLCHAIN=local MY_PLUGIN_REQUIRE_TOOLS=1
# The plugin's go.mod replaces the root module with ../.., so the build uses the root module of the same commit.
WORKDIR /src/plugins/my-media-imagemagick
COPY go.mod go.sum /src/
COPY plugins/my-media-imagemagick/go.mod plugins/my-media-imagemagick/go.sum ./
RUN go mod download
COPY gen /src/gen
COPY internal /src/internal
COPY plugins/my-media-imagemagick ./
CMD ["go", "test", "-race", "-count=1", "./..."]

FROM test AS build
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/my-media-imagemagick .

FROM deps AS runtime
COPY --from=build /out/my-media-imagemagick /usr/local/bin/my-media-imagemagick
USER nobody
EXPOSE 8080
ENTRYPOINT ["my-media-imagemagick"]
CMD ["-address", "http://:8080"]
