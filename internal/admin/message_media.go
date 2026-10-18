package admin

import (
	"context"
	"fmt"
	"net/http"

	"telesrv/internal/domain"
)

// The audit viewer shows message media to operators. Everything is read whole
// into memory, so each kind has a ceiling; anything larger is reported as
// unavailable rather than streamed.
const (
	MaxMessagePhotoBytes    = 10 << 20
	MaxMessageDocumentBytes = 20 << 20
	// messagePhotoThumbSide is the longest side a bubble thumbnail needs: the
	// largest stored size not exceeding it wins.
	messagePhotoThumbSide = 800
)

// MessageMediaFile is a stored message file prepared for the admin console.
// Attachment is set for anything that is not a safe inline image, so the panel
// serves it as a download instead of letting the browser render it.
type MessageMediaFile struct {
	Bytes      []byte
	MimeType   string
	Attachment bool
}

// MessagePhoto returns a message photo: a bubble-sized thumbnail, or the
// largest stored size when full is set. Only safe image types are returned.
func (s *Service) MessagePhoto(ctx context.Context, photoID int64, full bool) ([]byte, string, bool, error) {
	if s == nil || s.photos == nil || photoID <= 0 {
		return nil, "", false, nil
	}
	photo, found, err := s.photos.GetPhoto(ctx, photoID)
	if err != nil || !found {
		return nil, "", false, err
	}
	size, ok := pickMessagePhotoSize(photo.Sizes, full)
	if !ok {
		return nil, "", false, nil
	}
	data := size.Bytes
	if size.Kind != domain.PhotoSizeKindCached {
		chunk, found, err := s.photos.GetFile(ctx, domain.FileDownloadRequest{
			LocationKey: fmt.Sprintf("photo:%d:%s", photo.ID, size.Type),
			Limit:       MaxMessagePhotoBytes + 1,
		})
		if err != nil || !found {
			return nil, "", false, err
		}
		if chunk.Total <= 0 || chunk.Total > MaxMessagePhotoBytes || int64(len(chunk.Bytes)) != chunk.Total {
			return nil, "", false, nil
		}
		data = chunk.Bytes
	}
	if len(data) == 0 || len(data) > MaxMessagePhotoBytes {
		return nil, "", false, nil
	}
	mimeType := http.DetectContentType(data)
	if !safeAccountImageType(mimeType) {
		return nil, "", false, nil
	}
	return data, mimeType, true, nil
}

// pickMessagePhotoSize chooses among the downloadable (or cached) sizes: the
// largest one, or for a thumbnail the largest whose longer side fits a bubble,
// falling back to the smallest when every size is bigger.
func pickMessagePhotoSize(sizes []domain.PhotoSize, full bool) (domain.PhotoSize, bool) {
	var best, smallest domain.PhotoSize
	bestSide, smallestSide := -1, -1
	for _, size := range sizes {
		switch size.Kind {
		case domain.PhotoSizeKindDefault, domain.PhotoSizeKindProgressive, domain.PhotoSizeKindCached:
		default:
			continue
		}
		if !validAccountPhotoSizeType(size.Type) {
			continue
		}
		side := max(size.W, size.H)
		if smallestSide < 0 || side < smallestSide {
			smallest, smallestSide = size, side
		}
		if !full && side > messagePhotoThumbSide {
			continue
		}
		if side > bestSide {
			best, bestSide = size, side
		}
	}
	if bestSide >= 0 {
		return best, true
	}
	return smallest, smallestSide >= 0
}

// MessageDocument returns a document's thumbnail (thumbType is its one-letter
// size type) or, with thumbType empty, the stored file itself.
func (s *Service) MessageDocument(ctx context.Context, documentID int64, thumbType string) (MessageMediaFile, bool, error) {
	if s == nil || s.photos == nil || documentID <= 0 {
		return MessageMediaFile{}, false, nil
	}
	if thumbType != "" && !validDocumentThumbType(thumbType) {
		return MessageMediaFile{}, false, nil
	}
	key := fmt.Sprintf("doc:%d", documentID)
	limit := MaxMessageDocumentBytes
	if thumbType != "" {
		key += ":" + thumbType
		limit = MaxMessagePhotoBytes
	}
	chunk, found, err := s.photos.GetFile(ctx, domain.FileDownloadRequest{LocationKey: key, Limit: limit + 1})
	if err != nil || !found {
		return MessageMediaFile{}, false, err
	}
	if chunk.Total <= 0 || chunk.Total > int64(limit) || int64(len(chunk.Bytes)) != chunk.Total {
		return MessageMediaFile{}, false, nil
	}
	detected := http.DetectContentType(chunk.Bytes)
	if thumbType != "" {
		if !safeAccountImageType(detected) {
			return MessageMediaFile{}, false, nil
		}
		return MessageMediaFile{Bytes: chunk.Bytes, MimeType: detected}, true, nil
	}
	mimeType := chunk.MimeType
	if mimeType == "" {
		mimeType = detected
	}
	return MessageMediaFile{Bytes: chunk.Bytes, MimeType: mimeType, Attachment: true}, true, nil
}

// validDocumentThumbType admits the single lowercase letter MTProto uses for
// thumbnail sizes, so the value can never steer the file-store key elsewhere.
func validDocumentThumbType(value string) bool {
	return len(value) == 1 && value[0] >= 'a' && value[0] <= 'z'
}
