package modelcontext

import (
	"bytes"
	"encoding/binary"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/webp"
)

var pngEnd = []byte{0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82}

// IsViewableImage reports whether content is a complete PNG, JPEG, GIF, or WebP image of contentType,
// so a provider will accept it as model input.
func IsViewableImage(contentType string, content []byte) bool {
	if kind, _ := AttachmentKindForMediaType(contentType); kind != AttachmentKindImage {
		return false
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(content))
	if err != nil || "image/"+format != contentType {
		return false
	}
	return imageComplete(format, content)
}

// imageComplete checks the format's end marker, which a file truncated mid-write lacks even when its
// header decodes.
func imageComplete(format string, content []byte) bool {
	switch format {
	case "png":
		return bytes.HasSuffix(content, pngEnd)
	case "jpeg":
		return bytes.HasSuffix(bytes.TrimRight(content, "\x00\r\n"), []byte{0xff, 0xd9})
	case "gif":
		return content[len(content)-1] == 0x3b
	case "webp":
		// The RIFF size covers everything after its first 8 bytes; odd sizes are padded by one byte.
		size := int(binary.LittleEndian.Uint32(content[4:8])) + 8
		return len(content) == size || len(content) == size+1
	}
	return false
}
