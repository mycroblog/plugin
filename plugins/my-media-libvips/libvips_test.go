package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	mediav1 "github.com/mycroblog/plugin/gen/my/plugin/media/v1"
	"github.com/mycroblog/plugin/internal/media"
)

// The test binary is the worker of the converters of the tests too; the tests read the outputs with libvips.
func TestMain(m *testing.M) {
	if os.Getenv(workerEnv) == "1" {
		os.Exit(runWorker(os.Stdin, os.Stdout))
	}
	if err := vipsInit(1); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// secret stands for the location a photo hides in its metadata; no output may carry it.
const secret = "GPS 35.6895N 139.6917E"

func converter(t *testing.T) *Converter {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := &Converter{Worker: exe, WebPQuality: 80, JPEGQuality: 85, Threads: 2}
	if _, err := c.check(context.Background()); err != nil {
		t.Fatalf("check: %v", err)
	}
	return c
}

// attachment is the head of a post's image, with the boxes of the server's defaults.
func attachment(purpose mediav1.Purpose) *mediav1.ProcessHead {
	h := &mediav1.ProcessHead{
		Purpose: purpose,
		Limits:  &mediav1.Limits{MaxBytes: 16 << 20, MaxSide: 8192, MaxPixels: 7680 * 4320, MaxFrames: 500},
		Small:   &mediav1.Box{MaxPixels: 640 * 360},
		Medium:  &mediav1.Box{MaxPixels: 1920 * 1080},
		Large:   &mediav1.Box{MaxPixels: 3840 * 2160},
	}
	switch purpose {
	case mediav1.Purpose_PURPOSE_AVATAR:
		h.Small, h.Medium, h.Large = &mediav1.Box{Width: 96, Height: 96, Crop: true}, &mediav1.Box{Width: 192, Height: 192, Crop: true}, &mediav1.Box{Width: 400, Height: 400, Crop: true}
	case mediav1.Purpose_PURPOSE_EMOJI:
		h.Small, h.Medium, h.Large = &mediav1.Box{Width: 64, Height: 64}, &mediav1.Box{Width: 128, Height: 128}, &mediav1.Box{Width: 256, Height: 256}
	}
	return h
}

func process(t *testing.T, c *Converter, head *mediav1.ProcessHead, data []byte) media.ImageResult {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "input")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	head.Size = int64(len(data))
	res, err := c.Process(context.Background(), media.Image{Head: head, Path: path, Dir: dir})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	return res
}

// decoded is an output as libvips reads it back.
type decoded struct {
	width, pageHeight, pages int
	im                       *vimage
}

func decode(t *testing.T, a *arena, path string) decoded {
	t.Helper()
	format, err := media.SniffImageFile(path)
	if err != nil {
		t.Fatal(err)
	}
	im, err := a.load(int(format), path, format == mediav1.Format_FORMAT_GIF || format == mediav1.Format_FORMAT_WEBP)
	if err != nil {
		t.Fatalf("%s does not decode: %v", filepath.Base(path), err)
	}
	ph := im.pageHeight()
	return decoded{width: im.width(), pageHeight: ph, pages: im.height() / ph, im: im}
}

// pixel returns the RGB of the pixel at x, y of an image.
func pixel(t *testing.T, im *vimage, x, y int) [3]byte {
	t.Helper()
	px, err := im.crop(x, y, 1, 1)
	if err == nil {
		px, err = px.extractBands(0, 3)
	}
	var rgb []byte
	if err == nil {
		rgb, err = px.rgb()
	}
	if err != nil || len(rgb) != 3 {
		t.Fatalf("pixel %d,%d: %v", x, y, err)
	}
	return [3]byte(rgb)
}

// checkOutputs checks each output as the server does: its format is the declared one, its dimensions are the
// declared ones, it fits its box, and it carries no metadata.
func checkOutputs(t *testing.T, head *mediav1.ProcessHead, res media.ImageResult) {
	t.Helper()
	var a arena
	defer a.free()
	boxes := map[mediav1.Output]*mediav1.Box{
		mediav1.Output_OUTPUT_STILL_SMALL: head.GetSmall(), mediav1.Output_OUTPUT_ANIMATED_SMALL: head.GetSmall(),
		mediav1.Output_OUTPUT_STILL_MEDIUM: head.GetMedium(), mediav1.Output_OUTPUT_ANIMATED_MEDIUM: head.GetMedium(),
		mediav1.Output_OUTPUT_STILL_LARGE: head.GetLarge(), mediav1.Output_OUTPUT_ANIMATED_LARGE: head.GetLarge(),
	}
	for _, o := range res.Outputs {
		data, err := os.ReadFile(o.Path)
		if err != nil {
			t.Fatal(err)
		}
		if got := media.SniffImage(data); got != o.Format {
			t.Errorf("%v is %v, declared %v", o.Output, got, o.Format)
		}
		d := decode(t, &a, o.Path)
		w, h := int32(d.width), int32(d.pageHeight)
		if w != o.Width || h != o.Height {
			t.Errorf("%v is %d×%d, declared %d×%d", o.Output, w, h, o.Width, o.Height)
		}
		b := boxes[o.Output]
		fits := b.GetMaxPixels() > 0 && int64(w)*int64(h) <= b.GetMaxPixels() ||
			b.GetMaxPixels() == 0 && w <= b.GetWidth() && h <= b.GetHeight()
		if !fits {
			t.Errorf("%v, %d×%d, is outside its box %v", o.Output, w, h, b)
		}
		for _, leak := range []string{secret, "Exif\x00", "ICC_PROFILE", "iCCP", "eXIf", "tEXt", "zTXt", "iTXt", "XMP", "ICCP", "comment"} {
			if bytes.Contains(data, []byte(leak)) {
				t.Errorf("%v carries %q", o.Output, leak)
			}
		}
	}
}

func outputs(res media.ImageResult) map[mediav1.Output]media.Output {
	m := map[mediav1.Output]media.Output{}
	for _, o := range res.Outputs {
		m[o.Output] = o
	}
	return m
}

func TestPhoto(t *testing.T) {
	c := converter(t)
	// A 1200×800 photo, stored sideways: EXIF orientation 6 turns it upright to 800×1200.
	data := photo(t, 1200, 800, 6, iccProfile(t))
	head := attachment(mediav1.Purpose_PURPOSE_ATTACHMENT)
	res := process(t, c, head, data)

	if res.Outcome != mediav1.Outcome_OUTCOME_OK {
		t.Fatalf("outcome = %v", res.Outcome)
	}
	if res.Width != 800 || res.Height != 1200 {
		t.Errorf("dimensions = %d×%d, want the upright 800×1200", res.Width, res.Height)
	}
	if len(res.Blurhash) != 36 {
		t.Errorf("blurhash = %q", res.Blurhash)
	}
	got := outputs(res)
	if len(got) != 3 {
		t.Fatalf("%d outputs, want the 3 still ones", len(got))
	}
	for o, f := range map[mediav1.Output]mediav1.Format{
		mediav1.Output_OUTPUT_STILL_SMALL:  mediav1.Format_FORMAT_WEBP,
		mediav1.Output_OUTPUT_STILL_MEDIUM: mediav1.Format_FORMAT_WEBP,
		mediav1.Output_OUTPUT_STILL_LARGE:  mediav1.Format_FORMAT_JPEG,
	} {
		if got[o].Format != f {
			t.Errorf("%v is %v, want %v", o, got[o].Format, f)
		}
	}
	if s := got[mediav1.Output_OUTPUT_STILL_SMALL]; s.Width >= s.Height {
		t.Errorf("the small output is %d×%d, not upright", s.Width, s.Height)
	}
	checkOutputs(t, head, res)

	// The same image and head give the same bytes.
	again := process(t, c, attachment(mediav1.Purpose_PURPOSE_ATTACHMENT), data)
	for i, o := range res.Outputs {
		a, _ := os.ReadFile(o.Path)
		b, _ := os.ReadFile(again.Outputs[i].Path)
		if !bytes.Equal(a, b) {
			t.Errorf("%v differs between two calls", o.Output)
		}
	}
}

func TestWideGamutIsConverted(t *testing.T) {
	c := converter(t)
	icc := iccProfile(t)
	if icc == nil {
		skip(t, "no wide-gamut profile to test with (icc-profiles-free)")
	}
	// The same pixel values mean a more saturated green in Adobe RGB than in sRGB: converted, the green grows
	// stronger against the other channels; dropped as is, it stays where it was.
	res := process(t, c, attachment(mediav1.Purpose_PURPOSE_ATTACHMENT), solid(t, color.RGBA{60, 180, 60, 255}, icc))
	var a arena
	defer a.free()
	px := pixel(t, decode(t, &a, outputs(res)[mediav1.Output_OUTPUT_STILL_LARGE].Path).im, 32, 32)
	if r, g := int(px[0]), int(px[1]); g-r < 150 { // converted: about 180; dropped as is: about 120
		t.Errorf("green against red = %d against %d: the profile was dropped without converting", g, r)
	}
}

func TestTransparentAvatar(t *testing.T) {
	c := converter(t)
	img := image.NewNRGBA(image.Rect(0, 0, 500, 300))
	for i := range img.Pix {
		img.Pix[i] = byte(i % 251)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	head := attachment(mediav1.Purpose_PURPOSE_AVATAR)
	res := process(t, c, head, buf.Bytes())
	got := outputs(res)
	if l := got[mediav1.Output_OUTPUT_STILL_LARGE]; l.Format != mediav1.Format_FORMAT_PNG || l.Width != 300 || l.Height != 300 {
		t.Errorf("large = %v %d×%d, want a PNG cropped to 300×300", l.Format, l.Width, l.Height)
	}
	if s := got[mediav1.Output_OUTPUT_STILL_SMALL]; s.Width != 96 || s.Height != 96 {
		t.Errorf("small = %d×%d, want 96×96", s.Width, s.Height)
	}
	checkOutputs(t, head, res)
}

func TestOpaquePNGIsJPEG(t *testing.T) {
	c := converter(t)
	img := image.NewNRGBA(image.Rect(0, 0, 200, 100))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	head := attachment(mediav1.Purpose_PURPOSE_ATTACHMENT)
	res := process(t, c, head, buf.Bytes())
	if l := outputs(res)[mediav1.Output_OUTPUT_STILL_LARGE]; l.Format != mediav1.Format_FORMAT_JPEG {
		t.Errorf("large = %v, want JPEG for a PNG whose alpha is opaque all over", l.Format)
	}
	checkOutputs(t, head, res)
}

func TestEmojiIsPNG(t *testing.T) {
	c := converter(t)
	head := attachment(mediav1.Purpose_PURPOSE_EMOJI)
	res := process(t, c, head, photo(t, 300, 300, 1, nil))
	if l := outputs(res)[mediav1.Output_OUTPUT_STILL_LARGE]; l.Format != mediav1.Format_FORMAT_PNG {
		t.Errorf("large = %v, want PNG for an emoji", l.Format)
	}
	checkOutputs(t, head, res)
}

// animatedGIF encodes 3 frames of w × h, of stripes that move from frame to frame.
func animatedGIF(t *testing.T, w, h int) []byte {
	t.Helper()
	anim := &gif.GIF{}
	for i := range 3 {
		frame := image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{color.Black, color.White, color.RGBA{255, 0, 0, 255}})
		for p := range frame.Pix {
			frame.Pix[p] = uint8((p/40 + i) % 3)
		}
		anim.Image = append(anim.Image, frame)
		anim.Delay = append(anim.Delay, 10)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, anim); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestAnimation(t *testing.T) {
	c := converter(t)
	for _, purpose := range []mediav1.Purpose{mediav1.Purpose_PURPOSE_ATTACHMENT, mediav1.Purpose_PURPOSE_AVATAR} {
		t.Run(purpose.String(), func(t *testing.T) {
			head := attachment(purpose)
			res := process(t, c, head, animatedGIF(t, 520, 300))
			got := outputs(res)
			if len(got) != 6 {
				t.Fatalf("%d outputs, want 6", len(got))
			}
			if f := got[mediav1.Output_OUTPUT_ANIMATED_LARGE].Format; f != mediav1.Format_FORMAT_GIF {
				t.Errorf("animated large = %v, want GIF", f)
			}
			var a arena
			defer a.free()
			for _, o := range []mediav1.Output{mediav1.Output_OUTPUT_ANIMATED_SMALL, mediav1.Output_OUTPUT_ANIMATED_MEDIUM, mediav1.Output_OUTPUT_ANIMATED_LARGE} {
				if d := decode(t, &a, got[o].Path); d.pages != 3 {
					t.Errorf("%v has %d frames, want 3", o, d.pages)
				}
			}
			// The frames keep their delays, and move as they did.
			data, err := os.ReadFile(got[mediav1.Output_OUTPUT_ANIMATED_LARGE].Path)
			if err != nil {
				t.Fatal(err)
			}
			g, err := gif.DecodeAll(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(g.Delay, []int{10, 10, 10}) {
				t.Errorf("the delays of the GIF are %v, want 10 each", g.Delay)
			}
			if bytes.Equal(g.Image[0].Pix, g.Image[1].Pix) {
				t.Error("the first two frames of the GIF are the same")
			}
			checkOutputs(t, head, res)
		})
	}
}

func TestAnimatedWebP(t *testing.T) {
	c := converter(t)
	// An animated WebP made by libvips from the GIF.
	dir := t.TempDir()
	src := filepath.Join(dir, "in.gif")
	if err := os.WriteFile(src, animatedGIF(t, 120, 80), 0o600); err != nil {
		t.Fatal(err)
	}
	var a arena
	defer a.free()
	im, err := a.load(int(mediav1.Format_FORMAT_GIF), src, true)
	if err != nil {
		t.Fatal(err)
	}
	webp := filepath.Join(dir, "in.webp")
	if err := im.save("webp", webp, 90); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(webp)
	if err != nil {
		t.Fatal(err)
	}
	head := attachment(mediav1.Purpose_PURPOSE_ATTACHMENT)
	res := process(t, c, head, data)
	if res.Outcome != mediav1.Outcome_OUTCOME_OK || res.Width != 120 || res.Height != 80 {
		t.Fatalf("outcome %v, %d×%d", res.Outcome, res.Width, res.Height)
	}
	if len(res.Outputs) != 6 {
		t.Fatalf("%d outputs, want 6", len(res.Outputs))
	}
	checkOutputs(t, head, res)
}

func TestAPNGIsItsDefaultImage(t *testing.T) {
	c := converter(t)
	// A PNG with an acTL chunk before its image data reads as an APNG; libvips reads its default image.
	img := image.NewNRGBA(image.Rect(0, 0, 120, 80))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+3] = 255, 255
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	ihdrEnd := 8 + 8 + 13 + 4
	actl := binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, 1), 0)
	data = slices.Concat(data[:ihdrEnd], pngChunk("acTL", actl), data[ihdrEnd:])
	head := attachment(mediav1.Purpose_PURPOSE_ATTACHMENT)
	res := process(t, c, head, data)
	if res.Outcome != mediav1.Outcome_OUTCOME_OK || res.Width != 120 || res.Height != 80 || len(res.Outputs) != 3 {
		t.Fatalf("outcome %v, %d×%d, %d outputs; want OK, 120×80, the 3 still ones", res.Outcome, res.Width, res.Height, len(res.Outputs))
	}
	if slices.Contains(c.InputFormats(), mediav1.Format_FORMAT_APNG) {
		t.Error("APNG is declared")
	}
	checkOutputs(t, head, res)
}

func pngChunk(typ string, data []byte) []byte {
	c := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	c = append(append(c, typ...), data...)
	return binary.BigEndian.AppendUint32(c, crc32.ChecksumIEEE(c[4:]))
}

func TestHEIFFormats(t *testing.T) {
	c := converter(t)
	for _, tt := range []struct {
		format mediav1.Format
		file   string
	}{
		{mediav1.Format_FORMAT_AVIF, "sample.avif"},
		{mediav1.Format_FORMAT_HEIC, "sample.heic"},
	} {
		t.Run(tt.format.String(), func(t *testing.T) {
			if !slices.Contains(c.InputFormats(), tt.format) {
				skip(t, "this libvips does not decode "+tt.format.String()+" (libheif and its decoder)")
			}
			data, err := samples.ReadFile("samples/" + tt.file)
			if err != nil {
				t.Fatal(err)
			}
			head := attachment(mediav1.Purpose_PURPOSE_ATTACHMENT)
			res := process(t, c, head, data)
			if res.Outcome != mediav1.Outcome_OUTCOME_OK || res.Width != 64 || res.Height != 48 {
				t.Fatalf("outcome %v, %d×%d; want OK, 64×48", res.Outcome, res.Width, res.Height)
			}
			if len(res.Outputs) != 3 {
				t.Errorf("%d outputs, want the 3 still ones", len(res.Outputs))
			}
			checkOutputs(t, head, res)
		})
	}
}

func TestHEICIsRotatedOnce(t *testing.T) {
	c := converter(t)
	if !slices.Contains(c.InputFormats(), mediav1.Format_FORMAT_HEIC) {
		skip(t, "this libvips does not decode HEIC (libheif and libde265)")
	}
	// A 64×48 image, red above and blue below, that heif-enc stored with the transformation of EXIF orientation 6
	// (irot) and the EXIF itself: libheif turns it, and the EXIF must not turn it again.
	data, err := os.ReadFile("testdata/rotated.heic")
	if err != nil {
		t.Fatal(err)
	}
	head := attachment(mediav1.Purpose_PURPOSE_ATTACHMENT)
	res := process(t, c, head, data)
	if res.Outcome != mediav1.Outcome_OUTCOME_OK || res.Width != 48 || res.Height != 64 {
		t.Fatalf("outcome %v, %d×%d; want OK, 48×64", res.Outcome, res.Width, res.Height)
	}
	var a arena
	defer a.free()
	px := pixel(t, decode(t, &a, outputs(res)[mediav1.Output_OUTPUT_STILL_LARGE].Path).im, 45, 32)
	if px[0] < 200 || px[2] > 60 {
		t.Errorf("the right of the output is %v, want red: turned once, clockwise", px)
	}
	checkOutputs(t, head, res)
}

func TestUndeclaredFormatRefused(t *testing.T) {
	c := converter(t)
	c.inputFormats = nil
	data, err := samples.ReadFile("samples/sample.heic")
	if err != nil {
		t.Fatal(err)
	}
	if res := process(t, c, attachment(mediav1.Purpose_PURPOSE_ATTACHMENT), data); res.Outcome != mediav1.Outcome_OUTCOME_UNSUPPORTED_FORMAT {
		t.Errorf("outcome = %v, want OUTCOME_UNSUPPORTED_FORMAT", res.Outcome)
	}
}

func TestRefused(t *testing.T) {
	c := converter(t)
	good := photo(t, 64, 64, 1, nil)
	small := attachment(mediav1.Purpose_PURPOSE_ATTACHMENT)
	small.Limits.MaxSide = 32
	twoFrames := attachment(mediav1.Purpose_PURPOSE_ATTACHMENT)
	twoFrames.Limits.MaxFrames = 2
	fewPixels := attachment(mediav1.Purpose_PURPOSE_ATTACHMENT)
	// The canvas passes, the 3 frames together do not.
	fewPixels.Limits.MaxPixels = 120 * 80 * 2
	for _, tt := range []struct {
		name string
		head *mediav1.ProcessHead
		data []byte
		want mediav1.Outcome
	}{
		{"BMP", attachment(mediav1.Purpose_PURPOSE_ATTACHMENT), []byte("BM\x00\x00\x00\x00\x00\x00\x00\x00"), mediav1.Outcome_OUTCOME_UNSUPPORTED_FORMAT},
		{"SVG", attachment(mediav1.Purpose_PURPOSE_ATTACHMENT), []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), mediav1.Outcome_OUTCOME_UNSUPPORTED_FORMAT},
		{"JPEG header only", attachment(mediav1.Purpose_PURPOSE_ATTACHMENT), good[:20], mediav1.Outcome_OUTCOME_CORRUPT},
		{"JPEG cut short", attachment(mediav1.Purpose_PURPOSE_ATTACHMENT), good[:len(good)*2/3], mediav1.Outcome_OUTCOME_CORRUPT},
		{"PNG signature only", attachment(mediav1.Purpose_PURPOSE_ATTACHMENT), []byte("\x89PNG\r\n\x1A\n"), mediav1.Outcome_OUTCOME_CORRUPT},
		{"GIF header only", attachment(mediav1.Purpose_PURPOSE_ATTACHMENT), []byte("GIF89a"), mediav1.Outcome_OUTCOME_CORRUPT},
		{"wider than the limit", small, good, mediav1.Outcome_OUTCOME_DIMENSIONS},
		{"more frames than the limit", twoFrames, animatedGIF(t, 120, 80), mediav1.Outcome_OUTCOME_DIMENSIONS},
		{"more pixels in the frames than the limit", fewPixels, animatedGIF(t, 120, 80), mediav1.Outcome_OUTCOME_DIMENSIONS},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if res := process(t, c, tt.head, tt.data); res.Outcome != tt.want || len(res.Outputs) != 0 {
				t.Errorf("outcome = %v with %d outputs, want %v and none", res.Outcome, len(res.Outputs), tt.want)
			}
		})
	}
}

func TestCancelKillsTheWorker(t *testing.T) {
	c := converter(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "input")
	data := photo(t, 64, 64, 1, nil)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	head := attachment(mediav1.Purpose_PURPOSE_ATTACHMENT)
	head.Size = int64(len(data))
	start := time.Now()
	if _, err := c.Process(ctx, media.Image{Head: head, Path: path, Dir: dir}); err == nil {
		t.Error("a cancelled call succeeded")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("a cancelled call took %v", d)
	}
}

// photo encodes a w × h JPEG with EXIF (the orientation and the secret), a comment, and the ICC profile icc if any.
func photo(t *testing.T, w, h int, orientation uint16, icc []byte) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), 100, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return withSegments(buf.Bytes(), orientation, icc)
}

// solid encodes a 64×64 JPEG of one color with the ICC profile icc.
func solid(t *testing.T, c color.RGBA, icc []byte) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	return withSegments(buf.Bytes(), 1, icc)
}

// withSegments inserts, after the SOI marker of a JPEG, an APP1 EXIF segment with the orientation and the secret in
// ImageDescription, an APP2 segment with the ICC profile, and a COM segment with the secret.
func withSegments(jpg []byte, orientation uint16, icc []byte) []byte {
	le := binary.LittleEndian
	desc := append([]byte(secret), 0)
	tiff := []byte("II*\x00")
	tiff = le.AppendUint32(tiff, 8)
	tiff = le.AppendUint16(tiff, 2)
	// ImageDescription (0x010E), ASCII, its text after the IFD.
	tiff = le.AppendUint16(tiff, 0x010E)
	tiff = le.AppendUint16(tiff, 2)
	tiff = le.AppendUint32(tiff, uint32(len(desc)))
	tiff = le.AppendUint32(tiff, 8+2+2*12+4)
	// Orientation (0x0112), SHORT.
	tiff = le.AppendUint16(tiff, 0x0112)
	tiff = le.AppendUint16(tiff, 3)
	tiff = le.AppendUint32(tiff, 1)
	tiff = le.AppendUint16(tiff, orientation)
	tiff = le.AppendUint16(tiff, 0)
	tiff = le.AppendUint32(tiff, 0)
	tiff = append(tiff, desc...)

	var segs []byte
	segs = append(segs, segment(0xE1, append([]byte("Exif\x00\x00"), tiff...))...)
	if icc != nil {
		segs = append(segs, segment(0xE2, append([]byte("ICC_PROFILE\x00\x01\x01"), icc...))...)
	}
	segs = append(segs, segment(0xFE, []byte("comment: "+secret))...)
	return append(append(append([]byte{}, jpg[:2]...), segs...), jpg[2:]...)
}

func segment(marker byte, payload []byte) []byte {
	s := []byte{0xFF, marker}
	s = binary.BigEndian.AppendUint16(s, uint16(len(payload)+2))
	return append(s, payload...)
}

// iccProfile returns a wide-gamut ICC profile from the system, or nil.
func iccProfile(t *testing.T) []byte {
	t.Helper()
	for _, p := range []string{"/usr/share/color/icc/compatibleWithAdobeRGB1998.icc", "/usr/share/color/icc/colord/AdobeRGB1998.icc"} {
		if b, err := os.ReadFile(p); err == nil {
			return b
		}
	}
	return nil
}

// skip skips a test that needs what the plugin's image has, unless MY_PLUGIN_REQUIRE_TOOLS is set, as in the test
// stage of the Dockerfile, where a missing piece is a failure: CI must never pass by skipping.
func skip(t *testing.T, why string) {
	t.Helper()
	if os.Getenv("MY_PLUGIN_REQUIRE_TOOLS") != "" {
		t.Fatal(why + ", and MY_PLUGIN_REQUIRE_TOOLS is set")
	}
	t.Skip(why)
}
