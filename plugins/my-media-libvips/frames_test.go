package main

import (
	"bufio"
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"testing"
)

func TestGIFFrames(t *testing.T) {
	pal := color.Palette{color.Black, color.White}
	anim := &gif.GIF{
		Image: []*image.Paletted{
			image.NewPaletted(image.Rect(0, 0, 100, 50), pal),
			image.NewPaletted(image.Rect(10, 10, 30, 40), pal),
		},
		Delay:  []int{10, 10},
		Config: image.Config{Width: 100, Height: 50},
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, anim); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	info, err := gifFrames(bufio.NewReader(bytes.NewReader(data)))
	if err != nil || info.frames != 2 || info.pixels != 100*50+20*30 {
		t.Errorf("gifFrames = %+v, %v; want 2 frames of %d pixels", info, err, 100*50+20*30)
	}
	// Cut short in the second frame: the first is kept, as decoders keep it.
	info, err = gifFrames(bufio.NewReader(bytes.NewReader(data[:len(data)-8])))
	if err != nil || info.frames < 1 {
		t.Errorf("cut short: gifFrames = %+v, %v", info, err)
	}
	if _, err := gifFrames(bufio.NewReader(bytes.NewReader([]byte("GIF89a")))); err == nil {
		t.Error("a header alone is not malformed")
	}
}
