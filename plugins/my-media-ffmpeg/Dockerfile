# syntax=docker/dockerfile:1
# Build from the repository root: docker build -f plugins/my-media-ffmpeg/Dockerfile .
FROM golang:1.27.4-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY plugins/my-media-ffmpeg/go.mod plugins/my-media-ffmpeg/go.sum* ./plugins/my-media-ffmpeg/
RUN go work init . ./plugins/my-media-ffmpeg && go mod download
COPY gen ./gen
COPY internal ./internal
COPY plugins/my-media-ffmpeg ./plugins/my-media-ffmpeg
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/my-media-ffmpeg ./plugins/my-media-ffmpeg

FROM debian:trixie-slim
RUN apt-get update \
	&& apt-get install -y --no-install-recommends ffmpeg \
	&& rm -rf /var/lib/apt/lists/*
COPY --from=build /out/my-media-ffmpeg /usr/local/bin/my-media-ffmpeg
USER nobody
EXPOSE 8080
ENTRYPOINT ["my-media-ffmpeg"]
CMD ["-address", "http://:8080"]
