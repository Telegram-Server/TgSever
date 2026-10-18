package admin

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"telesrv/internal/domain"
)

// mediaResolver serves photos and stored files from maps, so the tests pin
// which size and which location key the admin viewer reads.
type mediaResolver struct {
	AvatarResolver
	photos map[int64]domain.Photo
	files  map[string]domain.FileChunk
	asked  []string
}

func (r *mediaResolver) GetPhoto(_ context.Context, id int64) (domain.Photo, bool, error) {
	photo, ok := r.photos[id]
	return photo, ok, nil
}

func (r *mediaResolver) GetFile(_ context.Context, req domain.FileDownloadRequest) (domain.FileChunk, bool, error) {
	r.asked = append(r.asked, req.LocationKey)
	chunk, ok := r.files[req.LocationKey]
	if !ok {
		return domain.FileChunk{}, false, nil
	}
	if req.Limit > 0 && len(chunk.Bytes) > req.Limit {
		chunk.Bytes = chunk.Bytes[:req.Limit]
	}
	return chunk, true, nil
}

var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 64)...)

func storedFile(data []byte, mime string) domain.FileChunk {
	return domain.FileChunk{Bytes: data, MimeType: mime, Total: int64(len(data))}
}

func newMediaService(resolver *mediaResolver) *Service {
	return NewService(Dependencies{Commands: newMemoryCommandRepo(), Photos: resolver, Now: fixedNow})
}

func TestMessagePhotoPicksAThumbnailOrTheFullSize(t *testing.T) {
	resolver := &mediaResolver{
		photos: map[int64]domain.Photo{7: {ID: 7, Sizes: []domain.PhotoSize{
			{Kind: domain.PhotoSizeKindStripped, Type: "i", Bytes: []byte{1, 2, 3}},
			{Kind: domain.PhotoSizeKindDefault, Type: "m", W: 320, H: 240},
			{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 800, H: 600},
			{Kind: domain.PhotoSizeKindProgressive, Type: "y", W: 1280, H: 960},
		}}},
		files: map[string]domain.FileChunk{
			"photo:7:x": storedFile(pngBytes, "image/png"),
			"photo:7:y": storedFile(pngBytes, "image/png"),
		},
	}
	svc := newMediaService(resolver)

	data, mime, found, err := svc.MessagePhoto(context.Background(), 7, false)
	if err != nil || !found || mime != "image/png" || !bytes.Equal(data, pngBytes) {
		t.Fatalf("thumbnail = %d bytes %q found=%v err=%v", len(data), mime, found, err)
	}
	if got := resolver.asked[len(resolver.asked)-1]; got != "photo:7:x" {
		t.Fatalf("thumbnail read %q, want the largest size that fits a bubble (x, 800px)", got)
	}

	if _, _, found, err := svc.MessagePhoto(context.Background(), 7, true); err != nil || !found {
		t.Fatalf("full size found=%v err=%v", found, err)
	}
	if got := resolver.asked[len(resolver.asked)-1]; got != "photo:7:y" {
		t.Fatalf("full size read %q, want the largest size (y)", got)
	}
}

func TestMessagePhotoRefusesWhatItCannotSafelyServe(t *testing.T) {
	resolver := &mediaResolver{
		photos: map[int64]domain.Photo{
			8: {ID: 8, Sizes: []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 800, H: 600}}},
			9: {ID: 9, Sizes: []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 800, H: 600}}},
		},
		files: map[string]domain.FileChunk{
			"photo:8:x": storedFile([]byte("<html><script>alert(1)</script></html>"), "text/html"),
			"photo:9:x": storedFile(append(pngBytes, make([]byte, MaxMessagePhotoBytes)...), "image/png"),
		},
	}
	svc := newMediaService(resolver)
	for _, id := range []int64{8, 9, 404} {
		if data, _, found, err := svc.MessagePhoto(context.Background(), id, false); err != nil || found || data != nil {
			t.Fatalf("photo %d = %d bytes found=%v err=%v, want not found", id, len(data), found, err)
		}
	}
}

func TestMessageDocumentServesAThumbnailOrTheFileAsAnAttachment(t *testing.T) {
	pdf := []byte("%PDF-1.7 report body")
	resolver := &mediaResolver{files: map[string]domain.FileChunk{
		"doc:21":   storedFile(pdf, "application/pdf"),
		"doc:21:m": storedFile(pngBytes, "image/jpeg"),
	}}
	svc := newMediaService(resolver)

	thumb, found, err := svc.MessageDocument(context.Background(), 21, "m")
	if err != nil || !found || thumb.MimeType != "image/png" || thumb.Attachment || !bytes.Equal(thumb.Bytes, pngBytes) {
		t.Fatalf("thumbnail = %+v found=%v err=%v, want the detected inline image", thumb, found, err)
	}
	file, found, err := svc.MessageDocument(context.Background(), 21, "")
	if err != nil || !found || !file.Attachment || file.MimeType != "application/pdf" || !bytes.Equal(file.Bytes, pdf) {
		t.Fatalf("file = %+v found=%v err=%v, want the stored file as an attachment", file, found, err)
	}
}

func TestMessageDocumentRejectsBadThumbTypesAndOversizedFiles(t *testing.T) {
	resolver := &mediaResolver{files: map[string]domain.FileChunk{
		"doc:22": storedFile(make([]byte, MaxMessageDocumentBytes+1), "video/mp4"),
	}}
	svc := newMediaService(resolver)
	for _, thumb := range []string{"../x", "mm", "1", "M"} {
		if _, found, err := svc.MessageDocument(context.Background(), 22, thumb); err != nil || found {
			t.Fatalf("thumb %q found=%v err=%v, want rejected", thumb, found, err)
		}
	}
	for _, key := range resolver.asked {
		if key != fmt.Sprintf("doc:%d", 22) {
			t.Fatalf("asked the file store for %q: a rejected thumb type must never reach it", key)
		}
	}
	if _, found, err := svc.MessageDocument(context.Background(), 22, ""); err != nil || found {
		t.Fatalf("oversized file found=%v err=%v, want not found", found, err)
	}
}
