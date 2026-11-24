package media

import (
	"bytes"
	"math/rand/v2"
	"testing"

	mediav1 "github.com/mycroblog/plugin/gen/my/plugin/media/v1"
)

func TestSniffImage(t *testing.T) {
	for _, tt := range []struct {
		head string
		want mediav1.Format
	}{
		{"\xFF\xD8\xFF\xE0", mediav1.Format_FORMAT_JPEG},
		{"\x89PNG\r\n\x1A\n\x00", mediav1.Format_FORMAT_PNG},
		{"GIF89a", mediav1.Format_FORMAT_GIF},
		{"RIFF\x00\x00\x00\x00WEBPVP8 ", mediav1.Format_FORMAT_WEBP},
		{"\x00\x00\x00\x1CftypavifX", mediav1.Format_FORMAT_UNSPECIFIED},
		{"<svg", mediav1.Format_FORMAT_UNSPECIFIED},
		{"", mediav1.Format_FORMAT_UNSPECIFIED},
	} {
		if got := SniffImage([]byte(tt.head)); got != tt.want {
			t.Errorf("SniffImage(%q) = %v, want %v", tt.head, got, tt.want)
		}
	}
}

func TestPlan(t *testing.T) {
	for _, tt := range []struct {
		name   string
		w, h   int
		box    *mediav1.Box
		fx, fy float64
		want   Target
	}{
		{"area, smaller", 640, 360, &mediav1.Box{MaxPixels: 640 * 360}, 0, 0, Target{Rect{0, 0, 640, 360}, 640, 360}},
		{"area, larger", 4000, 3000, &mediav1.Box{MaxPixels: 640 * 360}, 0, 0, Target{Rect{0, 0, 4000, 3000}, 554, 415}},
		{"square crop, centered", 400, 200, &mediav1.Box{Width: 96, Height: 96, Crop: true}, 0, 0, Target{Rect{100, 0, 200, 200}, 96, 96}},
		{"square crop, focus right", 400, 200, &mediav1.Box{Width: 96, Height: 96, Crop: true}, 1, 0, Target{Rect{200, 0, 200, 200}, 96, 96}},
		{"square crop, focus top", 200, 400, &mediav1.Box{Width: 96, Height: 96, Crop: true}, 0, 1, Target{Rect{0, 0, 200, 200}, 96, 96}},
		{"crop smaller than the box", 50, 80, &mediav1.Box{Width: 96, Height: 96, Crop: true}, 0, 0, Target{Rect{0, 15, 50, 50}, 50, 50}},
		{"within", 3000, 1000, &mediav1.Box{Width: 750, Height: 250}, 0, 0, Target{Rect{0, 0, 3000, 1000}, 750, 250}},
		{"within, taller", 1000, 1000, &mediav1.Box{Width: 750, Height: 250}, 0, 0, Target{Rect{0, 0, 1000, 1000}, 250, 250}},
		{"no bounds", 9000, 10, &mediav1.Box{}, 0, 0, Target{Rect{0, 0, 9000, 10}, 9000, 10}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := Plan(tt.w, tt.h, tt.box, tt.fx, tt.fy); got != tt.want {
				t.Errorf("Plan = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestPlanFits checks what the server checks of every output: it fits its box, and it is not larger than its crop.
func TestPlanFits(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 20000 {
		w, h := 1+r.IntN(9000), 1+r.IntN(9000)
		var b *mediav1.Box
		switch r.IntN(3) {
		case 0:
			b = &mediav1.Box{MaxPixels: int64(1 + r.IntN(4000*4000))}
		default:
			b = &mediav1.Box{Width: int32(1 + r.IntN(4000)), Height: int32(1 + r.IntN(4000)), Crop: r.IntN(2) == 0}
		}
		tg := Plan(w, h, b, r.Float64()*2-1, r.Float64()*2-1)
		fits := int64(tg.Width)*int64(tg.Height) <= b.GetMaxPixels()
		if b.GetMaxPixels() == 0 {
			fits = tg.Width <= int(b.GetWidth()) && tg.Height <= int(b.GetHeight())
		}
		c := tg.Crop
		if !fits || tg.Width < 1 || tg.Height < 1 || tg.Width > c.Width || tg.Height > c.Height ||
			c.X < 0 || c.Y < 0 || c.X+c.Width > w || c.Y+c.Height > h {
			if b.GetMaxPixels() > 0 && b.GetMaxPixels() < 2 && tg.Width*tg.Height == 1 {
				continue // a box of one pixel holds a one-pixel image
			}
			t.Fatalf("Plan(%d, %d, %v) = %+v", w, h, b, tg)
		}
	}
}

func TestBlurhash(t *testing.T) {
	// The expected hashes are those of the server's built-in processor for the same pixels, so that a client shows the
	// same placeholder whichever processor made the image.
	red := bytes.Repeat([]byte{255, 0, 0}, 32*32)
	grad := make([]byte, 0, 64*48*3)
	for y := range 48 {
		for x := range 64 {
			grad = append(grad, byte(x*4), byte(y*5), 128)
		}
	}
	for _, tt := range []struct {
		name string
		rgb  []byte
		w, h int
		want string
	}{
		{"solid red", red, 32, 32, "U9TI:j|cfQ|c|co1fQo1fQfQfQfQ|co1fQo1"},
		{"gradient", grad, 64, 48, "U#HLF[2Y$5SgmGazjtf7gJfjfQfjnla|jtfQ"},
	} {
		if got := Blurhash(tt.rgb, tt.w, tt.h, 4, 4); got != tt.want {
			t.Errorf("Blurhash(%s) = %q, want %q", tt.name, got, tt.want)
		}
	}
}
