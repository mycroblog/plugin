package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/draw"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"

	mediav1 "github.com/mycroblog/plugin/gen/my/plugin/media/v1"
)

// ImageMagick reads an APNG only through its video delegate, FFmpeg, which the policy turns off. The plugin reads it
// itself instead: apngInspect reads the frames from the chunks, without decoding a pixel, for the limits, and
// apngFrames composes each frame on the canvas (APNG 1.0: its dispose and blend operations) and writes it to a PNG of
// its own, which ImageMagick reads as an animation of whole frames.

// apng is the structure of an APNG, read from its chunks.
type apng struct {
	width, height int
	// ihdr is the data of the IHDR chunk, which every frame shares but for its size.
	ihdr []byte
	// shared are the chunks that every frame shares, whole, in their order: the palette, the transparency, and the
	// color space (PLTE, tRNS, iCCP, sRGB, gAMA, cHRM, cICP, sBIT).
	shared [][]byte
	hasICC bool
	// plays is the number of times the animation plays; 0 is forever.
	plays  int
	frames []apngFrame
}

// apngFrame is one frame: its fcTL, and the chunks of its image data.
type apngFrame struct {
	width, height, x, y int
	delayNum, delayDen  int
	dispose, blend      byte
	// data are the offsets and lengths of its image data in the file: of IDAT chunks, or of fdAT chunks after their
	// sequence number.
	data []span
}

type span struct{ off, n int64 }

const (
	apngDisposeNone       = 0
	apngDisposeBackground = 1
	apngDisposePrevious   = 2
	apngBlendSource       = 0
)

// sharedChunks are the chunks before the image data that each frame's own PNG repeats.
var sharedChunks = map[string]bool{
	"PLTE": true, "tRNS": true, "iCCP": true, "sRGB": true, "gAMA": true, "cHRM": true, "cICP": true, "sBIT": true,
}

// apngInspect reads the chunks of the APNG at path, without their image data. It refuses an APNG that breaks the
// format as OUTCOME_CORRUPT.
func apngInspect(path string) (*apng, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := &offsetReader{r: bufio.NewReader(f)}
	var sig [8]byte
	if _, err := io.ReadFull(r, sig[:]); err != nil || !bytes.Equal(sig[:], []byte("\x89PNG\r\n\x1A\n")) {
		return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "apng: no PNG signature")
	}
	a := &apng{}
	declared := -1
	var current *apngFrame
	// defaultIsFrame is set when a fcTL comes before the IDAT chunks: the default image is then the first frame.
	seenIDAT, afterIDAT, defaultIsFrame := false, false, false
	nextSeq := uint32(0)
	for {
		var header [8]byte
		if _, err := io.ReadFull(r, header[:]); err != nil {
			return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "apng: cut short before IEND")
		}
		n := int64(binary.BigEndian.Uint32(header[:4]))
		typ := string(header[4:8])
		if n > 1<<31-1 {
			return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "apng: a %s chunk of %d bytes", typ, n)
		}
		start := r.off
		// Only the small chunks are read whole: the image data stays in the file.
		var data []byte
		switch typ {
		case "IHDR", "acTL", "fcTL":
			data = make([]byte, n)
			if _, err := io.ReadFull(r, data); err != nil {
				return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "apng: a %s chunk cut short", typ)
			}
		default:
			if sharedChunks[typ] && !seenIDAT {
				data = make([]byte, n)
				if _, err := io.ReadFull(r, data); err != nil {
					return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "apng: a %s chunk cut short", typ)
				}
			} else if _, err := io.CopyN(io.Discard, r, n); err != nil {
				return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "apng: a %s chunk cut short", typ)
			}
		}
		if _, err := io.CopyN(io.Discard, r, 4); err != nil { // the CRC, which the PNG decoder checks for the image data
			return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "apng: a %s chunk cut short", typ)
		}

		if typ != "IDAT" && seenIDAT {
			afterIDAT = true
		}
		switch typ {
		case "IHDR":
			if len(data) != 13 || a.ihdr != nil {
				return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "apng: a bad IHDR")
			}
			a.ihdr = data
			a.width, a.height = int(binary.BigEndian.Uint32(data[0:4])), int(binary.BigEndian.Uint32(data[4:8]))
			if a.width <= 0 || a.height <= 0 || a.width > 1<<24 || a.height > 1<<24 {
				return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "apng: a canvas of %d×%d", a.width, a.height)
			}
		case "acTL":
			if len(data) != 8 || declared >= 0 || seenIDAT {
				return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "apng: a bad acTL")
			}
			declared = int(binary.BigEndian.Uint32(data[0:4]))
			a.plays = int(binary.BigEndian.Uint32(data[4:8]))
			if declared == 0 {
				return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "apng: an acTL of %d frames and %d plays", declared, a.plays)
			}
		case "fcTL":
			if len(data) != 26 || a.ihdr == nil {
				return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "apng: a bad fcTL")
			}
			if seq := binary.BigEndian.Uint32(data[0:4]); seq != nextSeq {
				return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "apng: sequence number %d, expected %d", seq, nextSeq)
			}
			nextSeq++
			if current != nil && len(current.data) == 0 {
				return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "apng: a frame without image data")
			}
			fr := apngFrame{
				width: int(binary.BigEndian.Uint32(data[4:8])), height: int(binary.BigEndian.Uint32(data[8:12])),
				x: int(binary.BigEndian.Uint32(data[12:16])), y: int(binary.BigEndian.Uint32(data[16:20])),
				delayNum: int(binary.BigEndian.Uint16(data[20:22])), delayDen: int(binary.BigEndian.Uint16(data[22:24])),
				dispose: data[24], blend: data[25],
			}
			if fr.width <= 0 || fr.height <= 0 || fr.x < 0 || fr.y < 0 ||
				fr.x+fr.width > a.width || fr.y+fr.height > a.height || fr.dispose > 2 || fr.blend > 1 {
				return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "apng: a frame outside the canvas, or a bad operation")
			}
			if !seenIDAT && len(a.frames) == 0 && (fr.x != 0 || fr.y != 0 || fr.width != a.width || fr.height != a.height) {
				return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "apng: the default image is a frame not of the canvas")
			}
			a.frames = append(a.frames, fr)
			current = &a.frames[len(a.frames)-1]
			if declared >= 0 && len(a.frames) > declared {
				return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "apng: more frames than its acTL, %d", declared)
			}
		case "IDAT":
			if afterIDAT {
				return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "apng: IDAT chunks apart")
			}
			seenIDAT = true
			// The default image is the first frame when a fcTL comes before it; otherwise it is not in the animation.
			if current != nil && len(a.frames) == 1 {
				defaultIsFrame = true
				current.data = append(current.data, span{start, n})
			}
		case "fdAT":
			if current == nil || !seenIDAT || n < 4 || defaultIsFrame && len(a.frames) == 1 {
				return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "apng: a fdAT without its fcTL")
			}
			// Its sequence number was skipped with the data: it is read again from the file.
			seq, err := readUint32At(f, start)
			if err != nil {
				return nil, err
			}
			if seq != nextSeq {
				return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "apng: sequence number %d, expected %d", seq, nextSeq)
			}
			nextSeq++
			current.data = append(current.data, span{start + 4, n - 4})
		case "IEND":
			if a.ihdr == nil || !seenIDAT || declared < 0 {
				return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "apng: no IHDR, IDAT, or acTL")
			}
			if len(a.frames) != declared {
				return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "apng: %d frames, its acTL says %d", len(a.frames), declared)
			}
			if len(current.data) == 0 {
				return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "apng: a frame without image data")
			}
			return a, nil
		default:
			if sharedChunks[typ] && !seenIDAT {
				a.shared = append(a.shared, chunk(typ, data))
				a.hasICC = a.hasICC || typ == "iCCP"
			}
		}
	}
}

// info returns what identify reads of another format: the canvas, the frames, and the pixels of all of them.
func (a *apng) info() imageInfo {
	info := imageInfo{width: a.width, height: a.height, frames: len(a.frames), hasICC: a.hasICC}
	for _, fr := range a.frames {
		info.pixels += int64(fr.width) * int64(fr.height)
	}
	return info
}

// apngFrames composes the frames of a on the canvas and writes each to a PNG in dir, which carries the shared chunks of
// the APNG, its color space among them. It returns the arguments that read them in ImageMagick as one animation.
func apngFrames(path, dir string, a *apng) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	deep := a.ihdr[8] == 16
	canvas := newCanvas(deep, image.Rect(0, 0, a.width, a.height))
	args := []string{"-loop", strconv.Itoa(a.plays), "-dispose", "None"}
	var saved draw.Image
	for i, fr := range a.frames {
		img, err := decodeFrame(f, a, fr)
		if err != nil {
			return nil, err
		}
		r := image.Rect(fr.x, fr.y, fr.x+fr.width, fr.y+fr.height)
		dispose := fr.dispose
		if i == 0 && dispose == apngDisposePrevious {
			dispose = apngDisposeBackground
		}
		if dispose == apngDisposePrevious {
			saved = newCanvas(deep, r)
			draw.Draw(saved, r, canvas, r.Min, draw.Src)
		}
		op := draw.Over
		if fr.blend == apngBlendSource {
			op = draw.Src
		}
		draw.Draw(canvas, r, img, img.Bounds().Min, op)

		name := filepath.Join(dir, fmt.Sprintf("frame-%05d.png", i))
		if err := writeFrame(name, canvas, a.shared); err != nil {
			return nil, err
		}
		num, den := fr.delayNum, fr.delayDen
		if den == 0 {
			den = 100
		}
		args = append(args, "-delay", fmt.Sprintf("%dx%d", num, den), "png:"+name)

		switch dispose {
		case apngDisposeBackground:
			draw.Draw(canvas, r, image.Transparent, image.Point{}, draw.Src)
		case apngDisposePrevious:
			draw.Draw(canvas, r, saved, r.Min, draw.Src)
		}
	}
	return args, nil
}

func newCanvas(deep bool, r image.Rectangle) draw.Image {
	if deep {
		return image.NewRGBA64(r)
	}
	return image.NewRGBA(r)
}

// decodeFrame decodes one frame: a PNG of its size, of the shared chunks and its image data.
func decodeFrame(f *os.File, a *apng, fr apngFrame) (image.Image, error) {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1A\n")
	ihdr := bytes.Clone(a.ihdr)
	binary.BigEndian.PutUint32(ihdr[0:4], uint32(fr.width))  //nolint:gosec // within the canvas
	binary.BigEndian.PutUint32(ihdr[4:8], uint32(fr.height)) //nolint:gosec // within the canvas
	b.Write(chunk("IHDR", ihdr))
	for _, c := range a.shared {
		b.Write(c)
	}
	for _, s := range fr.data {
		data := make([]byte, s.n)
		if _, err := f.ReadAt(data, s.off); err != nil {
			return nil, err
		}
		b.Write(chunk("IDAT", data))
	}
	b.Write(chunk("IEND", nil))
	img, err := png.Decode(&b)
	if err != nil {
		return nil, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "apng: a frame: %v", err)
	}
	return img, nil
}

// writeFrame writes img to a PNG at name, with the chunks of the color space after its IHDR.
func writeFrame(name string, img image.Image, shared [][]byte) error {
	var b bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&b, img); err != nil {
		return err
	}
	out := b.Bytes()
	// The IHDR chunk follows the signature: 8 bytes of length and type, 13 of data, and 4 of CRC.
	const afterIHDR = 8 + 8 + 13 + 4
	var w bytes.Buffer
	w.Write(out[:afterIHDR])
	for _, c := range shared {
		// The palette and the transparency are those of the APNG, not of the RGBA image the encoder writes.
		if t := string(c[4:8]); t != "PLTE" && t != "tRNS" && t != "sBIT" {
			w.Write(c)
		}
	}
	w.Write(out[afterIHDR:])
	return os.WriteFile(name, w.Bytes(), 0o600)
}

// chunk returns a whole PNG chunk: its length, type, data, and CRC.
func chunk(typ string, data []byte) []byte {
	c := make([]byte, 0, 12+len(data))
	c = binary.BigEndian.AppendUint32(c, uint32(len(data))) //nolint:gosec // a chunk's length fits
	c = append(c, typ...)
	c = append(c, data...)
	return binary.BigEndian.AppendUint32(c, crc32.ChecksumIEEE(c[4:]))
}

func readUint32At(f *os.File, off int64) (uint32, error) {
	var b [4]byte
	if _, err := f.ReadAt(b[:], off); err != nil {
		if errors.Is(err, io.EOF) {
			return 0, refuse(mediav1.Outcome_OUTCOME_CORRUPT, "apng: cut short")
		}
		return 0, err
	}
	return binary.BigEndian.Uint32(b[:]), nil
}

// offsetReader counts the bytes read from r.
type offsetReader struct {
	r   io.Reader
	off int64
}

func (o *offsetReader) Read(p []byte) (int, error) {
	n, err := o.r.Read(p)
	o.off += int64(n)
	return n, err
}
