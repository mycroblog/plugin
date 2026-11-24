package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	mediav1 "github.com/mycroblog/plugin/gen/my/plugin/media/v1"
	"github.com/mycroblog/plugin/internal/media"
)

// secret stands for the location a photo hides in its metadata; no output may carry it.
const secret = "GPS 35.6895N 139.6917E"

func converter(t *testing.T) *Converter {
	t.Helper()
	magick, err := exec.LookPath("magick")
	if err != nil {
		t.Skip("ImageMagick 7 (magick) is not installed")
	}
	c := &Converter{Magick: magick, SRGBProfile: "/usr/share/color/icc/sRGB.icc", WebPQuality: 80, JPEGQuality: 85}
	if err := c.check(); err != nil {
		t.Skipf("ImageMagick is not usable: %v", err)
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

// checkOutputs checks each output as the server does: its format is the declared one, its dimensions are the
// declared ones, it fits its box, and it carries no metadata.
func checkOutputs(t *testing.T, c *Converter, head *mediav1.ProcessHead, res media.ImageResult) {
	t.Helper()
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
		out, err := exec.Command(c.Magick, "identify", "-format", "%W %H\n", o.Path).Output()
		if err != nil {
			t.Fatalf("%v does not decode: %v", o.Output, err)
		}
		var w, h int32
		if _, err := fmt.Sscan(strings.SplitN(string(out), "\n", 2)[0], &w, &h); err != nil {
			t.Fatalf("%v: identify: %q", o.Output, out)
		}
		if w != o.Width || h != o.Height {
			t.Errorf("%v is %d×%d, declared %d×%d", o.Output, w, h, o.Width, o.Height)
		}
		b := boxes[o.Output]
		fits := b.GetMaxPixels() > 0 && int64(w)*int64(h) <= b.GetMaxPixels() ||
			b.GetMaxPixels() == 0 && w <= b.GetWidth() && h <= b.GetHeight()
		if !fits {
			t.Errorf("%v, %d×%d, is outside its box %v", o.Output, w, h, b)
		}
		for _, leak := range []string{secret, "Exif\x00", "ICC_PROFILE", "iCCP", "eXIf", "tEXt", "comment"} {
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
	checkOutputs(t, c, head, res)

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
		t.Skip("no wide-gamut profile to test with")
	}
	// The same pixel values mean a more saturated green in Adobe RGB than in sRGB: converted, the green grows
	// stronger against the other channels; dropped as is, it stays where it was.
	res := process(t, c, attachment(mediav1.Purpose_PURPOSE_ATTACHMENT), solid(t, color.RGBA{60, 180, 60, 255}, icc))
	large := outputs(res)[mediav1.Output_OUTPUT_STILL_LARGE]
	out, err := exec.Command(c.Magick, "identify", "-format", "%[fx:int(255*mean.r)] %[fx:int(255*mean.g)]", large.Path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var r, g int
	if _, err := fmt.Sscan(string(out), &r, &g); err != nil {
		t.Fatalf("mean: %q", out)
	}
	if g-r < 150 { // converted: about 180; dropped as is: about 120
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
	checkOutputs(t, c, head, res)
}

func TestEmojiIsPNG(t *testing.T) {
	c := converter(t)
	head := attachment(mediav1.Purpose_PURPOSE_EMOJI)
	res := process(t, c, head, photo(t, 300, 300, 1, nil))
	if l := outputs(res)[mediav1.Output_OUTPUT_STILL_LARGE]; l.Format != mediav1.Format_FORMAT_PNG {
		t.Errorf("large = %v, want PNG for an emoji", l.Format)
	}
	checkOutputs(t, c, head, res)
}

func TestAnimation(t *testing.T) {
	c := converter(t)
	anim := &gif.GIF{}
	for i := range 3 {
		frame := image.NewPaletted(image.Rect(0, 0, 120, 80), color.Palette{color.Black, color.White, color.RGBA{255, 0, 0, 255}})
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
	head := attachment(mediav1.Purpose_PURPOSE_ATTACHMENT)
	res := process(t, c, head, buf.Bytes())
	got := outputs(res)
	if len(got) != 6 {
		t.Fatalf("%d outputs, want 6", len(got))
	}
	if f := got[mediav1.Output_OUTPUT_ANIMATED_LARGE].Format; f != mediav1.Format_FORMAT_GIF {
		t.Errorf("animated large = %v, want GIF", f)
	}
	for _, o := range []mediav1.Output{mediav1.Output_OUTPUT_ANIMATED_SMALL, mediav1.Output_OUTPUT_ANIMATED_LARGE} {
		out, err := exec.Command(c.Magick, "identify", "-format", "x", got[o].Path).Output()
		if err != nil || len(out) != 3 {
			t.Errorf("%v has %d frames (%v), want 3", o, len(out), err)
		}
	}
	checkOutputs(t, c, head, res)
}

func TestRefused(t *testing.T) {
	c := converter(t)
	good := photo(t, 64, 64, 1, nil)
	small := attachment(mediav1.Purpose_PURPOSE_ATTACHMENT)
	small.Limits.MaxSide = 32
	for _, tt := range []struct {
		name string
		head *mediav1.ProcessHead
		data []byte
		want mediav1.Outcome
	}{
		{"BMP", attachment(mediav1.Purpose_PURPOSE_ATTACHMENT), []byte("BM\x00\x00\x00\x00\x00\x00\x00\x00"), mediav1.Outcome_OUTCOME_UNSUPPORTED_FORMAT},
		{"SVG", attachment(mediav1.Purpose_PURPOSE_ATTACHMENT), []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), mediav1.Outcome_OUTCOME_UNSUPPORTED_FORMAT},
		{"JPEG header only", attachment(mediav1.Purpose_PURPOSE_ATTACHMENT), good[:20], mediav1.Outcome_OUTCOME_CORRUPT},
		{"PNG signature only", attachment(mediav1.Purpose_PURPOSE_ATTACHMENT), []byte("\x89PNG\r\n\x1A\n"), mediav1.Outcome_OUTCOME_CORRUPT},
		{"wider than the limit", small, good, mediav1.Outcome_OUTCOME_DIMENSIONS},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if res := process(t, c, tt.head, tt.data); res.Outcome != tt.want || len(res.Outputs) != 0 {
				t.Errorf("outcome = %v with %d outputs, want %v and none", res.Outcome, len(res.Outputs), tt.want)
			}
		})
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
