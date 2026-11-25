# my-media-ffmpeg

A media plugin for [mycroblog/my](https://github.com/mycroblog/my) that removes the metadata of uploaded videos and audio
files with [FFmpeg](https://ffmpeg.org/), on a machine of your choice, in place of the server's built-in processor.

> [!NOTE]
> Not released yet: there is no published container image or version. Build the image from this repository
> (`docker build -f plugins/my-media-ffmpeg/Dockerfile .` at its root) to try it.

## Why use it

The server processes videos and audio files itself by default (`media_video = native`, `media_audio = native`), with
parsers of its own written in pure Go. This plugin does the same job with FFmpeg:

- **The work runs elsewhere.** Remuxing a 100 MiB video reads and writes all of it; the plugin keeps that off the
  instance.
- **FFmpeg's demuxers and muxers.** The ones most players and video services use, for the files of phones, cameras,
  and editors alike.

The server checks each upload with its built-in parsers before it calls the plugin, so the plugin receives the files
the built-in processor accepts: see [Accepted files](#accepted-files).

The plugin **remuxes**: it writes the same video and audio streams again, in the same container, without the metadata.
It never transcodes, so the quality and the size stay as they were, and a call takes about as long as reading the file.

## Quick start

Run the plugin with Docker, on a Unix socket that the server can reach:

```sh
docker run -d --name my-media-ffmpeg \
  -v /run/my:/run/my \
  ghcr.io/mycroblog/my-media-ffmpeg:latest \
  -address unix:///run/my/media-av.sock -socket-mode 0660
```

Point the server at it for videos, audio files, or both, and restart the server:

```sh
MY_MEDIA_VIDEO=plugin
MY_PLUGIN_MEDIA_VIDEO_ADDRESS=unix:///run/my/media-av.sock
MY_MEDIA_AUDIO=plugin
MY_PLUGIN_MEDIA_AUDIO_ADDRESS=unix:///run/my/media-av.sock
```

Check that the server and the plugin understand each other:

```sh
my plugin test --kind probe unix:///run/my/media-av.sock
my plugin test --kind media_video unix:///run/my/media-av.sock
my plugin test --kind media_audio unix:///run/my/media-av.sock
```

The media tests send a short MP4 and an MP3 that hide a location or a title in their metadata, and fail when it comes
back in an output.

This plugin takes videos and audio files only. Leave `media_image` as it is, or point it at
[my-media-imagemagick](../my-media-imagemagick).

## Connecting the server

The plugin listens on one address. Choose one by where the plugin runs:

| The plugin runs | Address | Who may connect |
|---|---|---|
| On the same machine | `unix:///run/my/media-av.sock` | Whoever may write to the socket (`-socket-mode`) |
| On another machine, over a network you do not fully trust | `https://0.0.0.0:9000` | Clients with a certificate of `-tls-client-ca-file`, or with the token of `-token-file` |
| On another machine, on a network that is yours alone | `http://0.0.0.0:8080` | Anyone who can reach the port |

Give the server the same address, with the host it reaches the plugin at, in `plugin_media_video_address` and
`plugin_media_audio_address`.

- **Prefer a Unix socket or `https://`.** Uploads carry their metadata, such as where a video was shot. Over `http://`
  they cross the network in the clear, and nothing proves to the server that it talks to this plugin. The server warns
  at start when you use `http://`.
- **The token.** Put the same token in a file for the plugin (`-token-file`) and in `plugin_media_video_token` and
  `plugin_media_audio_token` (or `MY_PLUGIN_MEDIA_VIDEO_TOKEN_FILE` and `MY_PLUGIN_MEDIA_AUDIO_TOKEN_FILE`) for the
  server. The server sends it over a Unix socket or `https://` only, so the plugin refuses to start with `-token-file`
  and an `http://` address.
- **Mutual TLS.** Give the plugin `-tls-client-ca-file`, and the server `plugin_media_video_tls_cert_file` and
  `plugin_media_video_tls_key_file` (and the same for audio), signed by that CA.

## Flags

| Flag | Default | Description |
|---|---|---|
| `-address` | (required) | Where to listen: `unix:///path`, `http://host:port`, or `https://host:port`. |
| `-socket-mode` | `0600` | Permissions of the Unix socket. Use `0660` and a shared group when the server runs as another user. |
| `-tls-cert-file`, `-tls-key-file` | | Certificate and key of the plugin. Required with `https://`. |
| `-tls-client-ca-file` | | Accept only clients with a certificate signed by this CA (mutual TLS). |
| `-token-file` | | Accept only requests that carry the token in this file. Not with `http://`. |
| `-concurrency` | number of CPUs | Files processed at once. Further calls wait for a free slot. |
| `-ffmpeg`, `-ffprobe` | `ffmpeg`, `ffprobe` | The FFmpeg commands, found in `PATH` unless a path is given. |
| `-shutdown-timeout` | `30s` | On `SIGTERM` or `SIGINT`, how long calls in flight may finish before the plugin exits. |

The server runs at most two video calls and two audio calls at once. Raise `-concurrency` only when several servers
share the plugin.

## What it does with a file

### Accepted files

| File | Container | Codecs |
|---|---|---|
| Video | MP4 (QuickTime `.mov` included), WebM | Video: H.264, VP8, VP9, AV1. Sound: AAC, Opus, Vorbis, MP3. |
| Audio | MP3 | MP3 |
| Audio | Ogg | Vorbis, Opus |
| Audio | M4A (MP4 with sound only) | AAC |
| Audio | WebM | Opus |

The plugin reads the container from the file's content, never from its name. A file in another container is refused as
an unsupported format; a file in an accepted container with a stream in another codec, such as HEVC or AC-3, is refused
as an unsupported codec.

### Output

- **The body**: the file again, in the container it came in (MP4 stays MP4, WebM stays WebM), with its video and audio
  streams as they were, bit for bit. An MP4 is written with its index at the front, so that browsers can start playing
  it before it has downloaded.
- **The cover**: when an audio file or an MP4 embeds a cover picture (ID3 `APIC`, MP4 `covr`), the plugin returns it as
  a separate output, re-encoded in its own format (JPEG or PNG) so that it carries no metadata either. The server then
  makes its variants as for any image.
- The same file gives the same bytes, so a retried call does not change the output. Upgrading FFmpeg may change them.

### Metadata

The body keeps only the video and audio streams. Everything else is dropped:

- tags of the file and of each stream: title, artist, album, comments, encoder, creation date;
- locations: the MP4 `©xyz` atom, Apple's QuickTime location keys, and GPS and other telemetry in data tracks (such as
  those of phones and action cameras);
- chapters, subtitles, attachments, timecode tracks, and the embedded cover (returned on its own, as above).

The plugin removes the metadata, not the server: the server checks the format, the codecs, and the size of the output,
but cannot tell that every trace of metadata is gone.

### Where the file goes

- The plugin keeps nothing. It works in a temporary directory of its own (under `TMPDIR`), removed at the end of each
  call, whether the call succeeds or not. A call needs about twice the size of the file there.
- Its logs carry the outcome and the duration of each call, never the file or its metadata.

## When a file is refused

The plugin answers each call with an outcome. The server rejects the upload, as its own processor would, for every
outcome but a temporary failure, which it retries later.

| Outcome | When | The server |
|---|---|---|
| `OUTCOME_OK` | The output follows. | Stores it. |
| `OUTCOME_UNSUPPORTED_FORMAT` | The file is in none of the accepted containers. | Rejects the upload. |
| `OUTCOME_UNSUPPORTED_CODEC` | A video or audio stream is in none of the accepted codecs. | Rejects the upload. |
| `OUTCOME_TOO_LARGE` | The file is longer than the server's limit. | Rejects the upload. |
| `OUTCOME_DIMENSIONS` | The video's pixels or frame rate exceed the server's limits. | Rejects the upload. |
| `OUTCOME_TOO_LONG` | The video or audio lasts longer than the server's limit. | Rejects the upload. |
| `OUTCOME_CORRUPT` | The file does not demux. | Rejects the upload. |
| `OUTCOME_TEMPORARY` | FFmpeg failed, the disk is full, or the plugin is shutting down. | Retries later. |

The plugin reads the duration, dimensions, and frame rate with `ffprobe` before it writes anything, so a file over the
limits costs little.

## Running it without Docker

You need FFmpeg 7 or later, with `ffmpeg` and `ffprobe`. The plugin only demuxes and muxes, so any build will do: it needs
no encoders but those of the cover pictures (`mjpeg`, `png`).

Build the plugin from a checkout of this repository, which it builds with (its `go.mod` replaces the shared module with
the checkout's), and run it:

```sh
git clone https://github.com/mycroblog/plugin.git
cd plugin/plugins/my-media-ffmpeg
GOWORK=off go build -o my-media-ffmpeg .
./my-media-ffmpeg -address unix:///run/my/media-av.sock
```

## License

[MIT](../../LICENSE). The container image includes FFmpeg, under the LGPL 2.1 or later, or the GPL for builds that
enable GPL components; see the image's `/usr/share/doc/ffmpeg`.
