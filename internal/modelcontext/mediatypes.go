package modelcontext

import (
	"path/filepath"
	"strings"
)

type mediaTypeInfo struct {
	Kind      string
	Extension string
}

const (
	AttachmentKindImage    = "image"
	AttachmentKindDocument = "document"
	AttachmentKindAudio    = "audio"

	mediaTypeImagePNG  = "image/png"
	mediaTypeImageJPEG = "image/jpeg"
	mediaTypeImageGIF  = "image/gif"
	mediaTypeImageWebP = "image/webp"

	mediaTypeAudioWAV  = "audio/wav"
	mediaTypeAudioMPEG = "audio/mpeg"
	mediaTypeAudioMP4  = "audio/mp4"
	mediaTypeAudioFLAC = "audio/flac"

	mediaTypeApplicationPDF     = "application/pdf"
	mediaTypeTextPlain          = "text/plain"
	mediaTypeTextMarkdown       = "text/markdown"
	mediaTypeTextCSV            = "text/csv"
	mediaTypeTextTSV            = "text/tab-separated-values"
	mediaTypeTextIIF            = "text/x-iif"
	mediaTypeApplicationDOC     = "application/msword"
	mediaTypeApplicationRTF     = "application/rtf"
	mediaTypeApplicationODT     = "application/vnd.oasis.opendocument.text"
	mediaTypeApplicationPages   = "application/vnd.apple.pages"
	mediaTypeApplicationKeynote = "application/vnd.apple.keynote"
	mediaTypeApplicationIWork   = "application/vnd.apple.iwork"
	mediaTypeApplicationPPT     = "application/vnd.ms-powerpoint"
	mediaTypeApplicationXLS     = "application/vnd.ms-excel"
	mediaTypeApplicationDOCX    = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	mediaTypeApplicationPPTX    = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	mediaTypeApplicationXLSX    = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
)

var attachmentMediaTypes = map[string]mediaTypeInfo{
	mediaTypeImagePNG:           {Kind: AttachmentKindImage, Extension: ".png"},
	mediaTypeImageJPEG:          {Kind: AttachmentKindImage, Extension: ".jpg"},
	mediaTypeImageGIF:           {Kind: AttachmentKindImage, Extension: ".gif"},
	mediaTypeImageWebP:          {Kind: AttachmentKindImage, Extension: ".webp"},
	mediaTypeAudioWAV:           {Kind: AttachmentKindAudio, Extension: ".wav"},
	mediaTypeAudioMPEG:          {Kind: AttachmentKindAudio, Extension: ".mp3"},
	mediaTypeAudioMP4:           {Kind: AttachmentKindAudio, Extension: ".m4a"},
	mediaTypeAudioFLAC:          {Kind: AttachmentKindAudio, Extension: ".flac"},
	mediaTypeApplicationPDF:     {Kind: AttachmentKindDocument, Extension: ".pdf"},
	mediaTypeTextPlain:          {Kind: AttachmentKindDocument, Extension: ".txt"},
	mediaTypeTextMarkdown:       {Kind: AttachmentKindDocument, Extension: ".md"},
	mediaTypeTextCSV:            {Kind: AttachmentKindDocument, Extension: ".csv"},
	mediaTypeTextTSV:            {Kind: AttachmentKindDocument, Extension: ".tsv"},
	mediaTypeTextIIF:            {Kind: AttachmentKindDocument, Extension: ".iif"},
	mediaTypeApplicationDOC:     {Kind: AttachmentKindDocument, Extension: ".doc"},
	mediaTypeApplicationRTF:     {Kind: AttachmentKindDocument, Extension: ".rtf"},
	mediaTypeApplicationODT:     {Kind: AttachmentKindDocument, Extension: ".odt"},
	mediaTypeApplicationPages:   {Kind: AttachmentKindDocument, Extension: ".pages"},
	mediaTypeApplicationKeynote: {Kind: AttachmentKindDocument, Extension: ".key"},
	mediaTypeApplicationIWork:   {Kind: AttachmentKindDocument, Extension: ".iwork"},
	mediaTypeApplicationPPT:     {Kind: AttachmentKindDocument, Extension: ".ppt"},
	mediaTypeApplicationXLS:     {Kind: AttachmentKindDocument, Extension: ".xls"},
	mediaTypeApplicationDOCX:    {Kind: AttachmentKindDocument, Extension: ".docx"},
	mediaTypeApplicationPPTX:    {Kind: AttachmentKindDocument, Extension: ".pptx"},
	mediaTypeApplicationXLSX:    {Kind: AttachmentKindDocument, Extension: ".xlsx"},
}

var attachmentMediaTypeAliases = map[string]string{
	"image/jpg":      mediaTypeImageJPEG,
	"audio/x-wav":    mediaTypeAudioWAV,
	"audio/wave":     mediaTypeAudioWAV,
	"audio/vnd.wave": mediaTypeAudioWAV,
	"audio/mp3":      mediaTypeAudioMPEG,
	"audio/x-m4a":    mediaTypeAudioMP4,
	"audio/m4a":      mediaTypeAudioMP4,
	"audio/x-flac":   mediaTypeAudioFLAC,
}

// CanonicalAttachmentMediaType maps common aliases reported by chat platforms
// and content sniffing to the media type Omnara stores.
func CanonicalAttachmentMediaType(mediaType string) string {
	if canonical, ok := attachmentMediaTypeAliases[mediaType]; ok {
		return canonical
	}
	return mediaType
}

// AudioInputFormat returns the Chat Completions input_audio format for an
// audio attachment media type.
func AudioInputFormat(mediaType string) (string, bool) {
	info, ok := attachmentMediaTypes[mediaType]
	if !ok || info.Kind != AttachmentKindAudio {
		return "", false
	}
	return info.Extension[1:], true
}

// AudioMediaTypeForFilename returns the stored audio media type for a filename
// with a supported audio extension.
func AudioMediaTypeForFilename(filename string) (string, bool) {
	extension := strings.ToLower(filepath.Ext(filename))
	for mediaType, info := range attachmentMediaTypes {
		if info.Kind == AttachmentKindAudio && info.Extension == extension {
			return mediaType, true
		}
	}
	return "", false
}

func AttachmentKindForMediaType(mediaType string) (string, bool) {
	info, ok := attachmentMediaTypes[mediaType]
	if !ok {
		return "", false
	}
	return info.Kind, true
}

func IsAttachmentMedia(mediaType string) bool {
	_, ok := attachmentMediaTypes[mediaType]
	return ok
}

func MediaFilename(filename, mediaType string) string {
	if filename != "" {
		return filename
	}
	if info, ok := attachmentMediaTypes[mediaType]; ok {
		return "attachment" + info.Extension
	}
	return "attachment"
}

func IsTextDocumentMediaType(mediaType string) bool {
	switch mediaType {
	case mediaTypeTextPlain, mediaTypeTextMarkdown, mediaTypeTextCSV, mediaTypeTextTSV, mediaTypeTextIIF:
		return true
	}
	return false
}
