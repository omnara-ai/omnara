package modelcontext

type mediaTypeInfo struct {
	Kind      string
	Extension string
}

const (
	AttachmentKindImage    = "image"
	AttachmentKindDocument = "document"

	mediaTypeImagePNG  = "image/png"
	mediaTypeImageJPEG = "image/jpeg"
	mediaTypeImageGIF  = "image/gif"
	mediaTypeImageWebP = "image/webp"

	mediaTypeApplicationPDF  = "application/pdf"
	mediaTypeTextPlain       = "text/plain"
	mediaTypeTextMarkdown    = "text/markdown"
	mediaTypeTextCSV         = "text/csv"
	mediaTypeTextTSV         = "text/tab-separated-values"
	mediaTypeApplicationDOCX = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	mediaTypeApplicationPPTX = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	mediaTypeApplicationXLSX = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
)

var attachmentMediaTypes = map[string]mediaTypeInfo{
	mediaTypeImagePNG:        {Kind: AttachmentKindImage, Extension: ".png"},
	mediaTypeImageJPEG:       {Kind: AttachmentKindImage, Extension: ".jpg"},
	mediaTypeImageGIF:        {Kind: AttachmentKindImage, Extension: ".gif"},
	mediaTypeImageWebP:       {Kind: AttachmentKindImage, Extension: ".webp"},
	mediaTypeApplicationPDF:  {Kind: AttachmentKindDocument, Extension: ".pdf"},
	mediaTypeTextPlain:       {Kind: AttachmentKindDocument, Extension: ".txt"},
	mediaTypeTextMarkdown:    {Kind: AttachmentKindDocument, Extension: ".md"},
	mediaTypeTextCSV:         {Kind: AttachmentKindDocument, Extension: ".csv"},
	mediaTypeTextTSV:         {Kind: AttachmentKindDocument, Extension: ".tsv"},
	mediaTypeApplicationDOCX: {Kind: AttachmentKindDocument, Extension: ".docx"},
	mediaTypeApplicationPPTX: {Kind: AttachmentKindDocument, Extension: ".pptx"},
	mediaTypeApplicationXLSX: {Kind: AttachmentKindDocument, Extension: ".xlsx"},
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
	case mediaTypeTextPlain, mediaTypeTextMarkdown, mediaTypeTextCSV, mediaTypeTextTSV:
		return true
	}
	return false
}
