package media

import (
	"bytes"
	"errors"
	"io"
	"os"

	mediav1 "github.com/mycroblog/plugin/gen/my/plugin/media/v1"
)

// SniffAV returns the container of a video or audio file from its leading bytes, or FORMAT_UNSPECIFIED for none of
// MP4 (QuickTime included), WebM, MP3, and Ogg. head should hold the first 512 bytes at least: a HEIF image, AVIF or
// HEIC, has a ftyp box too, and its brands tell it from an MP4.
func SniffAV(head []byte) mediav1.Format {
	switch {
	case len(head) >= 8 && string(head[4:8]) == "ftyp" && heifFormat(head) == mediav1.Format_FORMAT_UNSPECIFIED:
		return mediav1.Format_FORMAT_MP4
	case bytes.HasPrefix(head, []byte("\x1A\x45\xDF\xA3")) && bytes.Contains(head[:min(len(head), 64)], []byte("webm")):
		return mediav1.Format_FORMAT_WEBM
	case bytes.HasPrefix(head, []byte("OggS")):
		return mediav1.Format_FORMAT_OGG
	case bytes.HasPrefix(head, []byte("ID3")), len(head) >= 2 && head[0] == 0xFF && head[1]&0xE0 == 0xE0:
		return mediav1.Format_FORMAT_MP3
	}
	return mediav1.Format_FORMAT_UNSPECIFIED
}

// SniffAVFile returns the container of the file at path, as SniffAV does.
func SniffAVFile(path string) (mediav1.Format, error) {
	head, err := readHead(path, 512)
	if err != nil {
		return 0, err
	}
	return SniffAV(head), nil
}

// readHead returns the first n bytes of the file at path, or all of a shorter one.
func readHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	head := make([]byte, n)
	m, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return head[:m], nil
}
