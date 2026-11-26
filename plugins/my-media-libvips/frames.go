package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"os"

	mediav1 "github.com/mycroblog/plugin/gen/my/plugin/media/v1"
)

// libvips hands an animation over as whole frames, each of the canvas, and does not tell the size of the frames as
// the file stores them, which the limits bound together (media.proto, Limits). frameSizes reads them from the blocks
// of a GIF and the chunks of a WebP, without decoding a pixel, before libvips reads the file.

// frameInfo is what frameSizes reads of an animation.
type frameInfo struct {
	frames int
	// pixels is the sum of the pixels of every frame, as the file stores it.
	pixels int64
}

// errMalformed is a file whose structure breaks its format.
var errMalformed = errors.New("malformed")

// frameSizes returns the frames of a GIF or a WebP. A WebP of no ANMF chunk, a still image, has no frame here.
func frameSizes(path string, format mediav1.Format) (frameInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return frameInfo{}, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	switch format {
	case mediav1.Format_FORMAT_GIF:
		return gifFrames(r)
	case mediav1.Format_FORMAT_WEBP:
		return webpFrames(r)
	}
	return frameInfo{}, nil
}

// gifFrames walks the blocks of a GIF (GIF89a, section 17 on): every image descriptor is a frame. A file that ends
// without its trailer keeps the frames before, as decoders do.
func gifFrames(r *bufio.Reader) (frameInfo, error) {
	var head [13]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return frameInfo{}, errMalformed
	}
	if err := skipColorTable(r, head[10]); err != nil {
		return frameInfo{}, nil
	}
	var info frameInfo
	for {
		b, err := r.ReadByte()
		if err != nil {
			return info, nil
		}
		switch b {
		case 0x21: // An extension: its label, then its sub-blocks.
			if _, err := r.ReadByte(); err != nil {
				return info, nil
			}
			if err := skipSubBlocks(r); err != nil {
				return info, nil
			}
		case 0x2C: // An image descriptor: left, top, width, height, and its flags.
			var d [9]byte
			if _, err := io.ReadFull(r, d[:]); err != nil {
				return info, nil
			}
			info.frames++
			info.pixels += int64(binary.LittleEndian.Uint16(d[4:6])) * int64(binary.LittleEndian.Uint16(d[6:8]))
			if err := skipColorTable(r, d[8]); err != nil {
				return info, nil
			}
			// The minimum code size of the LZW data, then the data in sub-blocks.
			if _, err := r.ReadByte(); err != nil {
				return info, nil
			}
			if err := skipSubBlocks(r); err != nil {
				return info, nil
			}
		case 0x3B: // The trailer.
			return info, nil
		default:
			return info, errMalformed
		}
	}
}

// skipColorTable skips the color table that the flags of a logical screen or image descriptor announce.
func skipColorTable(r *bufio.Reader, flags byte) error {
	if flags&0x80 == 0 {
		return nil
	}
	_, err := r.Discard(3 << ((flags & 7) + 1))
	return err
}

func skipSubBlocks(r *bufio.Reader) error {
	for {
		n, err := r.ReadByte()
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		if _, err := r.Discard(int(n)); err != nil {
			return err
		}
	}
}

// webpFrames walks the chunks of a WebP (RIFF): every ANMF chunk is a frame, its size in bytes 6 to 11 of its data.
func webpFrames(r *bufio.Reader) (frameInfo, error) {
	var head [12]byte
	if _, err := io.ReadFull(r, head[:]); err != nil || string(head[:4]) != "RIFF" || string(head[8:]) != "WEBP" {
		return frameInfo{}, errMalformed
	}
	var info frameInfo
	for {
		var ch [8]byte
		if _, err := io.ReadFull(r, ch[:]); err != nil {
			return info, nil
		}
		size := int64(binary.LittleEndian.Uint32(ch[4:]))
		// Chunks are padded to an even length.
		skip := size + size&1
		if string(ch[:4]) == "ANMF" {
			if size < 16 {
				return info, errMalformed
			}
			var d [12]byte
			if _, err := io.ReadFull(r, d[:]); err != nil {
				return info, nil
			}
			w, h := uint24(d[6:9])+1, uint24(d[9:12])+1
			info.frames++
			info.pixels += int64(w) * int64(h)
			skip -= int64(len(d))
		}
		if _, err := r.Discard(int(skip)); err != nil {
			return info, nil
		}
	}
}

func uint24(b []byte) int { return int(b[0]) | int(b[1])<<8 | int(b[2])<<16 }
