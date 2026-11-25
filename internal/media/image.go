package media

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"strings"

	mediav1 "github.com/mycroblog/plugin/gen/my/plugin/media/v1"
)

// SniffImage returns the format of an image from its leading bytes, or FORMAT_UNSPECIFIED for none of JPEG, PNG, GIF,
// WebP, AVIF, and HEIC. head should hold the first 512 bytes at least, which cover the ftyp box of a HEIF file. An APNG is
// a PNG here: SniffImageFile tells it apart. File names and declared types are never consulted.
func SniffImage(head []byte) mediav1.Format {
	switch {
	case bytes.HasPrefix(head, []byte("\xFF\xD8\xFF")):
		return mediav1.Format_FORMAT_JPEG
	case bytes.HasPrefix(head, pngSignature):
		return mediav1.Format_FORMAT_PNG
	case bytes.HasPrefix(head, []byte("GIF87a")), bytes.HasPrefix(head, []byte("GIF89a")):
		return mediav1.Format_FORMAT_GIF
	case len(head) >= 12 && bytes.HasPrefix(head, []byte("RIFF")) && string(head[8:12]) == "WEBP":
		return mediav1.Format_FORMAT_WEBP
	}
	return heifFormat(head)
}

// SniffImageFile returns the format of the image in the file at path, as SniffImage does, and FORMAT_APNG for a PNG
// whose acTL chunk comes before its first IDAT chunk.
func SniffImageFile(path string) (mediav1.Format, error) {
	head, err := readHead(path, 512)
	if err != nil {
		return 0, err
	}
	format := SniffImage(head)
	if format != mediav1.Format_FORMAT_PNG {
		return format, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if _, err := f.Seek(int64(len(pngSignature)), io.SeekStart); err != nil {
		return 0, err
	}
	animated, err := pngAnimated(bufio.NewReader(f))
	if err != nil {
		return 0, err
	}
	if animated {
		return mediav1.Format_FORMAT_APNG, nil
	}
	return format, nil
}

var pngSignature = []byte("\x89PNG\r\n\x1A\n")

// pngAnimated reads the chunks of a PNG after its signature up to its first IDAT chunk, and reports whether an acTL
// chunk comes before it. A PNG cut short before its image data is not animated: its decoder refuses it.
func pngAnimated(r *bufio.Reader) (bool, error) {
	var header [8]byte
	for {
		if _, err := io.ReadFull(r, header[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return false, nil
			}
			return false, err
		}
		switch string(header[4:8]) {
		case "acTL":
			return true, nil
		case "IDAT", "IEND":
			return false, nil
		}
		// The data and the CRC.
		if _, err := r.Discard(int(binary.BigEndian.Uint32(header[:4])) + 4); err != nil {
			if errors.Is(err, io.EOF) {
				return false, nil
			}
			return false, err
		}
	}
}

// heifFormat returns FORMAT_AVIF or FORMAT_HEIC for a HEIF file, from the brands of its ftyp box: its major brand if
// that names one of them, or else its first compatible brand that does. Anything else is FORMAT_UNSPECIFIED, an MP4
// included.
func heifFormat(head []byte) mediav1.Format {
	if len(head) < 16 || string(head[4:8]) != "ftyp" {
		return mediav1.Format_FORMAT_UNSPECIFIED
	}
	size := int(binary.BigEndian.Uint32(head[:4]))
	if size < 16 || size > len(head) {
		size = len(head)
	}
	if f := heifBrand(string(head[8:12])); f != mediav1.Format_FORMAT_UNSPECIFIED {
		return f
	}
	for i := 16; i+4 <= size; i += 4 {
		if f := heifBrand(string(head[i : i+4])); f != mediav1.Format_FORMAT_UNSPECIFIED {
			return f
		}
	}
	return mediav1.Format_FORMAT_UNSPECIFIED
}

// heifBrand returns the format that one brand of a ftyp box names (ISO/IEC 23008-12, and AV1-ISOBMFF for AVIF).
func heifBrand(brand string) mediav1.Format {
	switch brand {
	case "avif", "avis":
		return mediav1.Format_FORMAT_AVIF
	case "heic", "heix", "heim", "heis", "hevc", "hevx":
		return mediav1.Format_FORMAT_HEIC
	}
	return mediav1.Format_FORMAT_UNSPECIFIED
}

// Rect is a rectangle of an image: its offset and its size, in pixels.
type Rect struct {
	X, Y, Width, Height int
}

// Target is how one output is made from an image: the part of the image it shows, and the size it is scaled to.
type Target struct {
	Crop          Rect
	Width, Height int
}

// Scaled reports whether the target changes the size of its crop.
func (t Target) Scaled() bool { return t.Width != t.Crop.Width || t.Height != t.Crop.Height }

// Plan returns the target of a w × h image in box b, with the focal point (fx, fy) of the head, as media.proto
// describes Box: with max_pixels, the image keeps its aspect ratio within that area; otherwise within width × height,
// first cropped to the aspect ratio of width × height around the focal point with crop. An image that fits is not
// enlarged, and a box of all zero bounds nothing.
func Plan(w, h int, b *mediav1.Box, fx, fy float64) Target {
	full := Rect{0, 0, w, h}
	bw, bh := int(b.GetWidth()), int(b.GetHeight())
	switch {
	case b.GetMaxPixels() > 0:
		area := int64(w) * int64(h)
		if area <= b.GetMaxPixels() {
			return Target{full, w, h}
		}
		s := math.Sqrt(float64(b.GetMaxPixels()) / float64(area))
		tw, th := max(1, int(float64(w)*s)), max(1, int(float64(h)*s))
		// Flooring keeps the area within the box; a side clamped to 1 may break it, so shrink the other.
		for int64(tw)*int64(th) > b.GetMaxPixels() && (tw > 1 || th > 1) {
			if tw >= th {
				tw--
			} else {
				th--
			}
		}
		return Target{full, tw, th}
	case bw <= 0 || bh <= 0:
		return Target{full, w, h}
	case b.GetCrop():
		c := cropRect(w, h, bw, bh, fx, fy)
		tw, th := fit(c.Width, c.Height, bw, bh)
		return Target{c, tw, th}
	}
	tw, th := fit(w, h, bw, bh)
	return Target{full, tw, th}
}

// fit scales w × h down to fit within bw × bh, keeping the aspect ratio as far as whole pixels allow.
func fit(w, h, bw, bh int) (int, int) {
	if w <= bw && h <= bh {
		return w, h
	}
	s := min(float64(bw)/float64(w), float64(bh)/float64(h))
	return min(bw, max(1, int(math.Round(float64(w)*s)))), min(bh, max(1, int(math.Round(float64(h)*s))))
}

// cropRect returns the largest part of a w × h image with the aspect ratio bw:bh, centered on the focal point as far
// as the image allows. The focal point goes from -1 (left, bottom) to 1 (right, top).
func cropRect(w, h, bw, bh int, fx, fy float64) Rect {
	cw, ch := w, h
	if int64(w)*int64(bh) > int64(h)*int64(bw) {
		cw = max(1, min(w, int(math.Round(float64(h)*float64(bw)/float64(bh)))))
	} else {
		ch = max(1, min(h, int(math.Round(float64(w)*float64(bh)/float64(bw)))))
	}
	if math.IsNaN(fx) || math.IsInf(fx, 0) {
		fx = 0
	}
	if math.IsNaN(fy) || math.IsInf(fy, 0) {
		fy = 0
	}
	fx, fy = max(-1, min(1, fx)), max(-1, min(1, fy))
	// The center of the crop, in pixels; y grows downwards in pixels and upwards in focal points.
	cx, cy := float64(w)*(1+fx)/2, float64(h)*(1-fy)/2
	x := max(0, min(w-cw, int(math.Round(cx-float64(cw)/2))))
	y := max(0, min(h-ch, int(math.Round(cy-float64(ch)/2))))
	return Rect{x, y, cw, ch}
}

// Blurhash encodes an image of w × h pixels, given as 8-bit RGB triplets row by row, with cx × cy components (1 to 9
// each), as https://github.com/woltapp/blurhash describes.
func Blurhash(rgb []byte, w, h, cx, cy int) string {
	if w <= 0 || h <= 0 || len(rgb) < w*h*3 {
		return ""
	}
	var lin [256]float64
	for i := range lin {
		lin[i] = srgbToLinear(i)
	}
	factors := make([][3]float64, cx*cy)
	cosX := make([]float64, w)
	cosY := make([]float64, h)
	for j := range cy {
		for y := range h {
			cosY[y] = math.Cos(math.Pi * float64(j) * float64(y) / float64(h))
		}
		for i := range cx {
			for x := range w {
				cosX[x] = math.Cos(math.Pi * float64(i) * float64(x) / float64(w))
			}
			var f [3]float64
			for y := range h {
				row := rgb[y*w*3:]
				for x := range w {
					b := cosX[x] * cosY[y]
					f[0] += b * lin[row[x*3]]
					f[1] += b * lin[row[x*3+1]]
					f[2] += b * lin[row[x*3+2]]
				}
			}
			norm := 2.0
			if i == 0 && j == 0 {
				norm = 1
			}
			scale := norm / float64(w*h)
			factors[j*cx+i] = [3]float64{f[0] * scale, f[1] * scale, f[2] * scale}
		}
	}

	var sb strings.Builder
	encode83(&sb, (cx-1)+(cy-1)*9, 1)
	maxAC := 0.0
	for _, f := range factors[1:] {
		maxAC = max(maxAC, math.Abs(f[0]), math.Abs(f[1]), math.Abs(f[2]))
	}
	quantMax := 0
	if len(factors) > 1 {
		quantMax = max(0, min(82, int(math.Floor(maxAC*166-0.5))))
		maxAC = float64(quantMax+1) / 166
	}
	encode83(&sb, quantMax, 1)
	dc := factors[0]
	encode83(&sb, linearToSRGB(dc[0])<<16|linearToSRGB(dc[1])<<8|linearToSRGB(dc[2]), 4)
	for _, f := range factors[1:] {
		q := func(v float64) int {
			return max(0, min(18, int(math.Floor(signPow(v/maxAC, 0.5)*9+9.5))))
		}
		encode83(&sb, q(f[0])*19*19+q(f[1])*19+q(f[2]), 2)
	}
	return sb.String()
}

const base83 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz#$%*+,-.:;=?@[]^_{|}~"

func encode83(sb *strings.Builder, v, digits int) {
	for i := digits - 1; i >= 0; i-- {
		d := v
		for range i {
			d /= 83
		}
		sb.WriteByte(base83[d%83])
	}
}

func srgbToLinear(v int) float64 {
	c := float64(v) / 255
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func linearToSRGB(v float64) int {
	c := max(0, min(1, v))
	if c <= 0.0031308 {
		return int(c*12.92*255 + 0.5)
	}
	return int((1.055*math.Pow(c, 1/2.4)-0.055)*255 + 0.5)
}

func signPow(v, e float64) float64 {
	return math.Copysign(math.Pow(math.Abs(v), e), v)
}
