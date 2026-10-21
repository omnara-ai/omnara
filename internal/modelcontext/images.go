package modelcontext

import (
	"bytes"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/webp"
)

// maxDecodedImagePixels bounds full decodes, whose memory grows with the dimensions a small file can
// declare. It covers phone photos and 8K screenshots.
const maxDecodedImagePixels = 40 << 20

// IsViewableImage reports whether content is a PNG, JPEG, GIF, or WebP image of contentType that decodes,
// so a provider will accept it as model input. Larger images are checked by their header only.
func IsViewableImage(contentType string, content []byte) bool {
	if kind, _ := AttachmentKindForMediaType(contentType); kind != AttachmentKindImage {
		return false
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(content))
	if err != nil || "image/"+format != contentType {
		return false
	}
	if config.Width*config.Height > maxDecodedImagePixels {
		return true
	}
	// A full decode rejects files truncated mid-write and accepts intact ones with trailing data,
	// such as JPEGs with an embedded video.
	_, _, err = image.Decode(bytes.NewReader(content))
	return err == nil
}
