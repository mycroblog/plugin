# syntax=docker/dockerfile:1
# Build from the repository root: docker build -f plugins/my-media-imagemagick/Dockerfile .
FROM golang:1.27.4-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY plugins/my-media-imagemagick/go.mod plugins/my-media-imagemagick/go.sum* ./plugins/my-media-imagemagick/
RUN go work init . ./plugins/my-media-imagemagick && go mod download
COPY gen ./gen
COPY internal ./internal
COPY plugins/my-media-imagemagick ./plugins/my-media-imagemagick
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/my-media-imagemagick ./plugins/my-media-imagemagick

FROM debian:trixie-slim
RUN apt-get update \
	&& apt-get install -y --no-install-recommends imagemagick icc-profiles-free \
	&& rm -rf /var/lib/apt/lists/*
COPY plugins/my-media-imagemagick/policy.xml /etc/ImageMagick-7/policy.xml
COPY --from=build /out/my-media-imagemagick /usr/local/bin/my-media-imagemagick
USER nobody
EXPOSE 8080
ENTRYPOINT ["my-media-imagemagick"]
CMD ["-address", "http://:8080"]
