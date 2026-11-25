package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mediav1 "github.com/mycroblog/plugin/gen/my/plugin/media/v1"
)

// testFrame is a frame of an APNG that buildAPNG writes: a solid rectangle.
type testFrame struct {
	rect            image.Rectangle
	c               color.NRGBA
	dispose, blend  byte
	delayNum, delay uint16
}

// buildAPNG writes an APNG of a w × h canvas. With defaultIsFrame, the default image is the first frame; without, it
// is a gray image of the canvas that is not in the animation. extra chunks go after the IHDR.
func buildAPNG(t *testing.T, w, h int, defaultIsFrame bool, frames []testFrame, extra ...[]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1A\n")
	ihdr := binary.BigEndian.AppendUint32(nil, uint32(w))
	ihdr = binary.BigEndian.AppendUint32(ihdr, uint32(h))
	ihdr = append(ihdr, 8, 6, 0, 0, 0) // 8-bit RGBA
	b.Write(chunk("IHDR", ihdr))
	for _, e := range extra {
		b.Write(e)
	}
	b.Write(chunk("acTL", append(binary.BigEndian.AppendUint32(nil, uint32(len(frames))), 0, 0, 0, 0)))
	seq := uint32(0)
	fctl := func(f testFrame) {
		d := binary.BigEndian.AppendUint32(nil, seq)
		for _, v := range []int{f.rect.Dx(), f.rect.Dy(), f.rect.Min.X, f.rect.Min.Y} {
			d = binary.BigEndian.AppendUint32(d, uint32(v))
		}
		d = binary.BigEndian.AppendUint16(d, f.delayNum)
		d = binary.BigEndian.AppendUint16(d, f.delay)
		d = append(d, f.dispose, f.blend)
		b.Write(chunk("fcTL", d))
		seq++
	}
	if !defaultIsFrame {
		b.Write(chunk("IDAT", idat(t, image.NewUniform(color.Gray{128}), image.Rect(0, 0, w, h))))
	}
	for i, f := range frames {
		fctl(f)
		data := idat(t, image.NewUniform(f.c), image.Rect(0, 0, f.rect.Dx(), f.rect.Dy()))
		if i == 0 && defaultIsFrame {
			b.Write(chunk("IDAT", data))
			continue
		}
		b.Write(chunk("fdAT", append(binary.BigEndian.AppendUint32(nil, seq), data...)))
		seq++
	}
	b.Write(chunk("IEND", nil))
	return b.Bytes()
}

// idat returns the image data of an 8-bit RGBA PNG of src within r: each row after the filter type None, compressed.
func idat(t *testing.T, src image.Image, r image.Rectangle) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zlib.NewWriter(&b)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := []byte{0}
		for x := r.Min.X; x < r.Max.X; x++ {
			c := color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA)
			row = append(row, c.R, c.G, c.B, c.A)
		}
		if _, err := z.Write(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func writeTemp(t *testing.T, data []byte) (path, dir string) {
	t.Helper()
	dir = t.TempDir()
	path = filepath.Join(dir, "input")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, dir
}

// pixel returns the color at x, y of the PNG at path.
func pixel(t *testing.T, path string, x, y int) color.NRGBA {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
}

var (
	red   = color.NRGBA{255, 0, 0, 255}
	green = color.NRGBA{0, 255, 0, 255}
	blue  = color.NRGBA{0, 0, 255, 255}
	clear = color.NRGBA{}
	half  = color.NRGBA{0, 0, 255, 128}
)

func TestAPNGCompose(t *testing.T) {
	// A red canvas; a green square on it, cleared after; a blue square, put back after; a half blue square, blended
	// over what the previous frames left.
	frames := []testFrame{
		{rect: image.Rect(0, 0, 8, 8), c: red, delayNum: 1, delay: 10},
		{rect: image.Rect(0, 0, 4, 4), c: green, dispose: apngDisposeBackground, delayNum: 2, delay: 10},
		{rect: image.Rect(4, 4, 8, 8), c: blue, dispose: apngDisposePrevious, delayNum: 3},
		{rect: image.Rect(2, 2, 6, 6), c: half, blend: 1, delayNum: 4, delay: 100},
	}
	path, dir := writeTemp(t, buildAPNG(t, 8, 8, true, frames))
	a, err := apngInspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if info := a.info(); info.width != 8 || info.height != 8 || info.frames != 4 || info.pixels != 64+16+16+16 {
		t.Errorf("info = %+v", info)
	}
	args, err := apngFrames(path, dir, a)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-loop", "0", "-dispose", "None",
		"-delay", "1x10", "png:" + filepath.Join(dir, "frame-00000.png"),
		"-delay", "2x10", "png:" + filepath.Join(dir, "frame-00001.png"),
		"-delay", "3x100", "png:" + filepath.Join(dir, "frame-00002.png"), // a denominator of 0 is 100
		"-delay", "4x100", "png:" + filepath.Join(dir, "frame-00003.png"),
	}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Errorf("args = %q\nwant   %q", args, want)
	}
	frame := func(i int) string { return filepath.Join(dir, "frame-0000"+string(rune('0'+i))+".png") }
	for _, tt := range []struct {
		frame, x, y int
		want        color.NRGBA
	}{
		{0, 0, 0, red},
		{1, 0, 0, green}, {1, 7, 7, red},
		// The green square was cleared to transparent.
		{2, 0, 0, clear}, {2, 7, 7, blue},
		// The blue square was put back to red; half blue over red, over transparent.
		{3, 5, 5, color.NRGBA{127, 0, 128, 255}}, {3, 2, 2, half}, {3, 7, 7, red}, {3, 0, 0, clear},
	} {
		if got := pixel(t, frame(tt.frame), tt.x, tt.y); !near(got, tt.want) {
			t.Errorf("frame %d at %d,%d = %v, want %v", tt.frame, tt.x, tt.y, got, tt.want)
		}
	}
}

func near(a, b color.NRGBA) bool {
	d := func(x, y uint8) bool { return x-y <= 1 || y-x <= 1 }
	return d(a.R, b.R) && d(a.G, b.G) && d(a.B, b.B) && d(a.A, b.A)
}

func TestAPNGDefaultImageOutside(t *testing.T) {
	// The default image, gray, is not a frame: the animation is the two that follow.
	frames := []testFrame{
		{rect: image.Rect(0, 0, 4, 4), c: red},
		{rect: image.Rect(0, 0, 4, 4), c: green},
	}
	path, dir := writeTemp(t, buildAPNG(t, 4, 4, false, frames))
	a, err := apngInspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apngFrames(path, dir, a); err != nil {
		t.Fatal(err)
	}
	if got := pixel(t, filepath.Join(dir, "frame-00000.png"), 0, 0); got != red {
		t.Errorf("first frame = %v, want red", got)
	}
}

func TestAPNGKeepsColorSpace(t *testing.T) {
	iccp := chunk("iCCP", []byte("profile\x00\x00compressed"))
	frames := []testFrame{{rect: image.Rect(0, 0, 2, 2), c: red}, {rect: image.Rect(0, 0, 2, 2), c: green}}
	path, dir := writeTemp(t, buildAPNG(t, 2, 2, true, frames, iccp))
	a, err := apngInspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if !a.info().hasICC {
		t.Error("hasICC = false")
	}
	if _, err := apngFrames(path, dir, a); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(filepath.Join(dir, "frame-00001.png"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, iccp) {
		t.Error("the frame lost the iCCP chunk")
	}
}

func TestAPNGCorrupt(t *testing.T) {
	two := []testFrame{{rect: image.Rect(0, 0, 4, 4), c: red}, {rect: image.Rect(0, 0, 4, 4), c: green}}
	good := buildAPNG(t, 4, 4, true, two)
	outside := buildAPNG(t, 4, 4, true, []testFrame{{rect: image.Rect(0, 0, 4, 4), c: red}, {rect: image.Rect(2, 2, 6, 6), c: green}})
	small := buildAPNG(t, 4, 4, true, []testFrame{{rect: image.Rect(0, 0, 2, 2), c: red}, {rect: image.Rect(0, 0, 4, 4), c: green}})
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{"cut short", good[:len(good)-20]},
		{"frame outside the canvas", outside},
		{"default image smaller than the canvas", small},
		{"more frames than its acTL", bytes.Replace(good, chunk("acTL", []byte{0, 0, 0, 2, 0, 0, 0, 0}), chunk("acTL", []byte{0, 0, 0, 1, 0, 0, 0, 0}), 1)},
		{"fewer frames than its acTL", bytes.Replace(good, chunk("acTL", []byte{0, 0, 0, 2, 0, 0, 0, 0}), chunk("acTL", []byte{0, 0, 0, 3, 0, 0, 0, 0}), 1)},
		{"sequence out of order", withSequence(t, good, 2, 5)},
		{"bad image data", corruptFrameData(good)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path, dir := writeTemp(t, tt.data)
			a, err := apngInspect(path)
			if err == nil {
				_, err = apngFrames(path, dir, a)
			}
			var r *errRefused
			if !errors.As(err, &r) || r.outcome != mediav1.Outcome_OUTCOME_CORRUPT {
				t.Errorf("err = %v, want OUTCOME_CORRUPT", err)
			}
		})
	}
}

// withSequence rewrites the sequence number old of a fdAT chunk of data to seq.
func withSequence(t *testing.T, data []byte, old, seq uint32) []byte {
	t.Helper()
	marker := append([]byte("fdAT"), binary.BigEndian.AppendUint32(nil, old)...)
	i := bytes.Index(data, marker)
	if i < 0 {
		t.Fatal("no such fdAT")
	}
	out := bytes.Clone(data)
	binary.BigEndian.PutUint32(out[i+4:], seq)
	return out
}

// corruptFrameData spoils the zlib stream of the last fdAT chunk.
func corruptFrameData(data []byte) []byte {
	out := bytes.Clone(data)
	i := bytes.LastIndex(out, []byte("fdAT"))
	for j := i + 8; j < i+16; j++ {
		out[j] ^= 0xFF
	}
	return out
}

func TestAPNGLimits(t *testing.T) {
	frames := make([]testFrame, 10)
	for i := range frames {
		frames[i] = testFrame{rect: image.Rect(0, 0, 4, 4), c: red}
	}
	path, _ := writeTemp(t, buildAPNG(t, 4, 4, true, frames))
	a, err := apngInspect(path)
	if err != nil {
		t.Fatal(err)
	}
	var r *errRefused
	if err := a.info().check(&mediav1.Limits{MaxFrames: 5}); !errors.As(err, &r) || r.outcome != mediav1.Outcome_OUTCOME_DIMENSIONS {
		t.Errorf("10 frames, at most 5: %v, want OUTCOME_DIMENSIONS", err)
	}
}
