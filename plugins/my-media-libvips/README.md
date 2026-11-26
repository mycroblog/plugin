# my-media-libvips

A media plugin for [mycroblog/my](https://github.com/mycroblog/my) that turns uploaded images into their variants with
[libvips](https://www.libvips.org/), on a machine of your choice, in place of the server's built-in image processor.

> [!NOTE]
> Not released yet: there is no published container image or version. Build the image from this repository
> (`docker build -f plugins/my-media-libvips/Dockerfile .` at its root) to try it.

## Why use it

The server processes images itself by default (`media_image = native`), in pure Go. This plugin does the same job with
libvips, and does it better where pure Go falls short. It does about what [my-media-imagemagick](../my-media-imagemagick)
does, with less CPU and memory:

| | Built-in processor | my-media-imagemagick | my-media-libvips |
|---|---|---|---|
| Formats | JPEG, PNG, GIF, and WebP | Also AVIF, HEIC, and animated PNG | Also AVIF and HEIC; an animated PNG is still, its default image |
| Wide-gamut photos (Display P3, Adobe RGB) | The color profile is dropped as is, so the colors come out dull or shifted | Converted to sRGB first | Converted to sRGB first |
| Resizing | Catmull-Rom for still images, bilinear for the frames of an animation | Lanczos, with a light sharpening after a large reduction | Lanczos, the colors premultiplied by the alpha, each frame of an animation on its own |
| WebP encoder | A pure-Go port | libwebp | libwebp |
| A 12-megapixel photo, on 2 CPUs | | 3.3 s, 190 MB at peak | 0.3 s, 135 MB at peak |
| Where the work runs | On the instance | Wherever you run the plugin | Wherever you run the plugin |

The figures are those of the plugins' own tests on one machine: a 4032×3024 JPEG, stored sideways with an Adobe RGB
profile, made into the three outputs of a post.

The sizes and the formats of the outputs are the ones the server asks for, so the clients and other servers see the same
kind of images as before; only better ones.

## Quick start

Run the plugin with Docker, on a Unix socket that the server can reach:

```sh
docker run -d --name my-media-libvips \
  -v /run/my:/run/my \
  ghcr.io/mycroblog/my-media-libvips:0.1 \
  -address unix:///run/my/media-image.sock -socket-mode 0660
```

Point the server at it, and restart the server:

```sh
MY_MEDIA_IMAGE=plugin
MY_PLUGIN_MEDIA_IMAGE_ADDRESS=unix:///run/my/media-image.sock
```

Check that the server and the plugin understand each other:

```sh
my plugin test --kind probe unix:///run/my/media-image.sock
my plugin test --kind media_image unix:///run/my/media-image.sock
```

`--kind media_image` sends a few test images that hide a location in their metadata, and fails when the location comes
back in an output.

This plugin takes images only. Leave `media_video` and `media_audio` as they are, or point them at
[my-media-ffmpeg](../my-media-ffmpeg).

## Connecting the server

The plugin listens on one address. Choose one by where the plugin runs:

| The plugin runs | Address | Who may connect |
|---|---|---|
| On the same machine | `unix:///run/my/media-image.sock` | Whoever may write to the socket (`-socket-mode`) |
| On another machine, over a network you do not fully trust | `https://0.0.0.0:9000` | Clients with a certificate of `-tls-client-ca-file`, or with the token of `-token-file` |
| On another machine, on a network that is yours alone | `http://0.0.0.0:8080` | Anyone who can reach the port |

Give the server the same address, with the host it reaches the plugin at, in `plugin_media_image_address`.

- **Prefer a Unix socket or `https://`.** Uploads carry their metadata, such as where a photo was taken. Over `http://`
  they cross the network in the clear, and nothing proves to the server that it talks to this plugin. The server warns
  at start when you use `http://`.
- **The token.** Put the same token in a file for the plugin (`-token-file`) and in `plugin_media_image_token` or
  `MY_PLUGIN_MEDIA_IMAGE_TOKEN_FILE` for the server. The server sends it over a Unix socket or `https://` only, so the
  plugin refuses to start with `-token-file` and an `http://` address.
- **Mutual TLS.** Give the plugin `-tls-client-ca-file`, and the server `plugin_media_image_tls_cert_file` and
  `plugin_media_image_tls_key_file`, signed by that CA.

## Flags

| Flag | Default | Description |
|---|---|---|
| `-address` | (required) | Where to listen: `unix:///path`, `http://host:port`, or `https://host:port`. |
| `-socket-mode` | `0600` | Permissions of the Unix socket. Use `0660` and a shared group when the server runs as another user. |
| `-tls-cert-file`, `-tls-key-file` | | Certificate and key of the plugin. Required with `https://`. |
| `-tls-client-ca-file` | | Accept only clients with a certificate signed by this CA (mutual TLS). |
| `-token-file` | | Accept only requests that carry the token in this file. Not with `http://`. |
| `-concurrency` | `2` | Images processed at once. Further calls wait for a free slot. The CPUs are shared among them: each image gets the number of CPUs divided by `-concurrency` threads, one at least. |
| `-webp-quality` | `80` | Quality of the WebP outputs, from 1 to 100. |
| `-jpeg-quality` | `85` | Quality of the JPEG outputs, from 1 to 100. |
| `-shutdown-timeout` | `30s` | On `SIGTERM` or `SIGINT`, how long calls in flight may finish. The calls still running then are cancelled, and their files removed, before the plugin exits. |

The server runs at most two image calls at once, which the default matches: each of them gets half the CPUs. Raise it
only when several servers share the plugin.

## What it does with an image

### Accepted images

| Format | Notes |
|---|---|
| JPEG | Its EXIF orientation is applied to the pixels first. |
| PNG | Still. An animated PNG is read as its default image, a still PNG, as the server reads one when the plugin does not declare APNG. |
| GIF | Still or animated. |
| WebP | Still or animated, lossy or lossless. |
| AVIF | Still: the primary image of the file. |
| HEIC | Still: the primary image of the file, turned and mirrored as the file says. |

The plugin reads the format from the file's content, never from its name, and hands it to the libvips loader of that
format alone. Every other loader of libvips, such as those of PDF, SVG, TIFF, and the one through ImageMagick, is
blocked in the plugin, so nothing else ever parses an upload.

The plugin accepts AVIF and HEIC only when its libvips decodes them, which it checks at start, logging the formats it
accepts. The container image has the decoders; see [Running it without Docker](#running-it-without-docker) for your
own installation.

### Outputs

The server says what the image is for (a post, an avatar, a header, or a custom emoji) and the size of each output. The
plugin makes:

| Output | Still image | Animated image |
|---|---|---|
| Small, medium | WebP (lossy, with transparency when the image has it) | A still WebP of the first frame, and an animated WebP |
| Large | JPEG when the image is opaque, PNG when it has transparency; always PNG for a custom emoji | A still JPEG or PNG of the first frame, and a GIF |

An image whose alpha channel is opaque all over counts as opaque. The quality of the WebP and the JPEG outputs is 80
and 85 by default, as the built-in processor's; `-webp-quality` and `-jpeg-quality` change it.

- An image smaller than its box is not enlarged.
- Avatars are cropped to a square around the focal point the person chose, every frame of an animation alike; the
  other purposes keep their aspect ratio.
- The plugin also returns the image's dimensions and the [blurhash](https://blurha.sh) of the small output, which the
  clients show while the image loads.
- The same image with the same request and the same flags gives the same bytes, so a retried call does not change the
  outputs.

### Metadata

Every output is written without metadata: EXIF (location, camera, date), XMP, IPTC, comments, and the color profile. The
pixels are converted to sRGB with the image's profile before it is dropped, so the colors stay as they were.

The plugin removes the metadata, not the server: the server checks the format and the size of each output, but cannot
tell that every trace of metadata is gone.

### Where the image goes

- Each image is processed by a process of its own, a worker, which the plugin starts for the call. A decoder that
  crashes on an image takes that call down, not the plugin; a cancelled call kills its worker; and the memory of an
  image goes back to the system when its worker exits.
- The plugin keeps nothing. It works in a temporary directory of its own (under `TMPDIR`), where the worker also writes
  the decoded pixels it reuses, removed at the end of each call, whether the call succeeds or not.
- Its logs carry the outcome and the duration of each call, never the image or its metadata.

## When an image is refused

The plugin answers each call with an outcome. The server rejects the upload, as its own processor would, for every
outcome but a temporary failure, which it retries later.

| Outcome | When | The server |
|---|---|---|
| `OUTCOME_OK` | The outputs follow. | Stores them. |
| `OUTCOME_UNSUPPORTED_FORMAT` | The image is none of the accepted formats. | Rejects the upload. |
| `OUTCOME_TOO_LARGE` | The file is longer than the server's limit. | Rejects the upload. |
| `OUTCOME_DIMENSIONS` | The width, height, pixels, or frames exceed the server's limits. | Rejects the upload. |
| `OUTCOME_CORRUPT` | The image does not decode, or is cut short. | Rejects the upload. |
| `OUTCOME_TEMPORARY` | libvips failed otherwise, its worker crashed or ran out of memory, or the plugin is shutting down. | Retries later. |

The plugin checks the server's limits against the header of the image before libvips decodes a pixel: the frames of a
GIF and of a WebP it counts and measures itself, from their blocks and chunks, so an image built to expand in memory
is refused instead of filling it.

## Running it without Docker

You need libvips 8.15 or later, built with JPEG, PNG, GIF (cgif), WebP, and lcms support, and its headers and
`pkg-config` to build the plugin, which links it (cgo). On Debian and Ubuntu, `libvips-dev` has them all. The plugin
checks libvips at start, and refuses to start without what it needs.

For AVIF and HEIC, libvips needs libheif with a decoder of each: on Debian and Ubuntu, `libheif-plugin-dav1d` (AVIF)
and `libheif-plugin-libde265` (HEIC). Without one, the plugin starts and does not declare that format.

Build the plugin from a checkout of this repository, which it builds with (its `go.mod` replaces the shared module with
the checkout's), and run it:

```sh
git clone https://github.com/mycroblog/plugin.git
cd plugin/plugins/my-media-libvips
GOWORK=off go build -o my-media-libvips .
./my-media-libvips -address unix:///run/my/media-image.sock
```

The binary is its own worker: it starts itself again, with `MY_MEDIA_LIBVIPS_WORKER=1` in the environment, for each
image. Keep it where it was started from while it runs.

## License

[MIT](../../LICENSE).
