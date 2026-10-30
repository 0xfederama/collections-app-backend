package images

import (
	"github.com/google/uuid"
)

// Attachment is a stored upload (DATA_MODEL §5). Inserts set every field; reads
// (GetAttachment) set only ID, the two storage keys, MediaType and SizeBytes.
type Attachment struct {
	ID                  uuid.UUID
	StorageKey          string
	ThumbnailStorageKey string
	MediaType           string
	SizeBytes           int64
	OriginalFilename    *string
	Width, Height       int
	SHA256              []byte
}

// FileKeys are the storage keys of the two files of an attachment.
type FileKeys struct {
	StorageKey          string
	ThumbnailStorageKey string
}

// ItemImageState is the image part of an item as it is read under a row lock:
// its version and the image it points to (an attachment, a URL or neither).
type ItemImageState struct {
	Version      int
	AttachmentID *uuid.UUID
	URL          *string
}
