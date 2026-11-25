# my-media-imagemagick

A media plugin for [mycroblog/my](https://github.com/mycroblog/my) that turns uploaded images into their variants with
[ImageMagick](https://imagemagick.org/), on a machine of your choice, in place of the server's built-in image processor.

> [!NOTE]
> Not released yet: there is no published container image or version. Build the image from this repository
> (`docker build -f plugins/my-media-imagemagick/Dockerfile .` at its root) to try it.

## Why use it

The server processes images itself by default (`media_image = native`), in pure Go. This plugin does the same job with
ImageMagick, and does it better where pure Go falls short:

| | Built-in processor | my-media-imagemagick |
|---|---|---|
| Formats | JPEG, PNG, GIF, and WebP | Also AVIF, HEIC (the photos of iPhones), and animated PNG |
| Wide-gamut photos (Display P3, Adobe RGB), such as those of recent phones | The color profile is dropped as is, so the colors come out dull or shifted | Converted to sRGB first, so the colors stay as they were |
| Resizing | Catmull-Rom for still images, bilinear for the frames of an animation | Lanczos for both, with a light sharpening after a large reduction |
| WebP encoder | A pure-Go port | libwebp, the reference encoder |
| Animated GIF output | Each frame mapped onto its original palette, without dithering | A palette made for each resized frame, with dithering, so gradients show fewer bands |
| Where the work runs | On the instance | Wherever you run the plugin, so large uploads do not slow down the instance |

The sizes and the formats of the outputs are the ones the server asks for, so the clients and other servers see the same
kind of images as before; only better ones.

## Quick start

Run the plugin with Docker, on a Unix socket that the server can reach:

```sh
docker run -d --name my-media-imagemagick \
  -v /run/my:/run/my \
  ghcr.io/mycroblog/my-media-imagemagick:latest \
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
| `-concurrency` | number of CPUs | Images processed at once. Further calls wait for a free slot. |
| `-webp-quality` | `80` | Quality of the WebP outputs, from 1 to 100. |
| `-jpeg-quality` | `85` | Quality of the JPEG outputs, from 1 to 100. |
| `-srgb-profile` | `/usr/share/color/icc/sRGB.icc` | The sRGB ICC profile that images with a profile of their own are converted to. |
| `-magick` | `magick` | The ImageMagick command, found in `PATH` unless a path is given. |
| `-shutdown-timeout` | `30s` | On `SIGTERM` or `SIGINT`, how long calls in flight may finish before the plugin exits. |

The server runs at most two image calls at once. Raise `-concurrency` only when several servers share the plugin.

## What it does with an image

### Accepted images

| Format | Notes |
|---|---|
| JPEG | Its EXIF orientation is applied to the pixels first. |
| PNG | Still. |
| APNG (animated PNG) | Animated, with its frames composed as the format says (their disposal and blending). |
| GIF | Still or animated. |
| WebP | Still or animated, lossy or lossless. |
| AVIF | Still: the primary image of the file. |
| HEIC | Still: the primary image of the file, turned and mirrored as the file says. |

The plugin reads the format from the file's content, never from its name, and hands ImageMagick only these.

The server accepts AVIF, HEIC, and APNG uploads only from a plugin that declares them, which this one does through
`Describe`, and only from a version of mycroblog/my that asks for it: one after
[mycroblog/my#3499](https://github.com/mycroblog/my/pull/3499). An earlier server, or one whose plugin is unreachable,
refuses AVIF and HEIC uploads and reads an APNG as a still PNG, as its own processor does.

- The plugin declares AVIF and HEIC only when its ImageMagick decodes them: at start, it decodes a small image of
  each, and logs the formats it accepts. The container image has the decoders; see
  [Running it without Docker](#running-it-without-docker) for your own installation.
- The plugin reads an APNG itself and hands ImageMagick its frames, since ImageMagick reads one only through FFmpeg.
  The server checks its frames and its size from its headers before it calls the plugin, and the plugin checks them
  again before it decodes a pixel.
- The image sequences of an AVIF or a HEIC (animated AVIF, HEIC bursts) give a still image of their primary image.

### Outputs

The server says what the image is for (a post, an avatar, a header, or a custom emoji) and the size of each output. The
plugin makes:

| Output | Still image | Animated image |
|---|---|---|
| Small, medium | WebP (lossy, with transparency when the image has it) | A still WebP of the first frame, and an animated WebP |
| Large | JPEG when the image is opaque, PNG when it has transparency; always PNG for a custom emoji | A still JPEG or PNG of the first frame, and a GIF |

The quality of the WebP and the JPEG outputs is 80 and 85 by default, as the built-in processor's; `-webp-quality` and
`-jpeg-quality` change it.

- An image smaller than its box is not enlarged.
- Avatars are cropped to a square around the focal point the person chose; the other purposes keep their aspect ratio.
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

- The plugin keeps nothing. It works in a temporary directory of its own (under `TMPDIR`), removed at the end of each
  call, whether the call succeeds or not.
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
| `OUTCOME_CORRUPT` | The image does not decode. | Rejects the upload. |
| `OUTCOME_TEMPORARY` | ImageMagick failed, ran out of memory, or the plugin is shutting down. | Retries later. |

The plugin applies the server's limits to ImageMagick itself, before decoding the whole image, so an image built to
expand in memory is refused instead of filling it.

## Running it without Docker

You need ImageMagick 7 (the `magick` command) built with JPEG, PNG, GIF, and WebP support and with lcms, and an sRGB
ICC profile (on Debian and Ubuntu, `icc-profiles-free` installs one where `-srgb-profile` looks by default). The plugin
checks all of this at start, and refuses to start without it.

```sh
magick -list format | grep -E '^ *(JPEG|PNG|GIF|WEBP)\*? '
```

For AVIF and HEIC, ImageMagick needs libheif with a decoder of each: on Debian and Ubuntu, `libheif-plugin-dav1d`
(AVIF) and `libheif-plugin-libde265` (HEIC). Without one, the plugin starts and does not declare that format.

Build the plugin from a checkout of this repository, which it builds with (its `go.mod` replaces the shared module with
the checkout's), and run it:

```sh
git clone https://github.com/mycroblog/plugin.git
cd plugin/plugins/my-media-imagemagick
GOWORK=off go build -o my-media-imagemagick .
./my-media-imagemagick -address unix:///run/my/media-image.sock
```

The container image ships a [`policy.xml`](policy.xml) that disables every ImageMagick coder but those of the formats
above (AVIF and HEIC for reading only), the ICC coder that reads the sRGB profile, and the raw RGB coder the blurhash is
read from, and every delegate, FFmpeg among them. Outside the container,
the plugin names the coder of each image itself, so it does not depend on your `policy.xml`; tightening that file as well
is still a good idea.

## License

[MIT](../../LICENSE).
