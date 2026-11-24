package media

import (
	"bytes"

	mediav1 "github.com/mycroblog/plugin/gen/my/plugin/media/v1"
)

// SniffAV returns the container of a video or audio file from its leading bytes, or FORMAT_UNSPECIFIED for none of
// MP4 (QuickTime included), WebM, MP3, and Ogg. head should hold the first 64 bytes at least.
func SniffAV(head []byte) mediav1.Format {
	switch {
	case len(head) >= 8 && string(head[4:8]) == "ftyp":
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
