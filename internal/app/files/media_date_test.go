package files

import (
	"bytes"
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// 频道导入要保留来源文件的日期：照片和文档都能带着一个过去的日期创建，不指定时仍取当前时间。
func TestImportedMediaKeepsTheSourceDate(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	thumbnailer := &fakeVideoThumbnailer{thumb: testJPEG(t, 320, 180)}
	svc := NewService(media, blobs, 2, WithVideoThumbnailer(thumbnailer))
	const past = 1_500_000_000
	now := int(time.Now().Unix())

	photo, err := svc.CreatePhotoFromBytesAt(ctx, testJPEG(t, 640, 360), past)
	if err != nil {
		t.Fatalf("CreatePhotoFromBytesAt: %v", err)
	}
	if photo.Date != past || len(photo.Sizes) == 0 {
		t.Fatalf("photo = date %d with %d sizes, want the source date", photo.Date, len(photo.Sizes))
	}
	recent, err := svc.CreatePhotoFromBytesAt(ctx, testJPEG(t, 640, 360), 0)
	if err != nil {
		t.Fatalf("CreatePhotoFromBytesAt without a date: %v", err)
	}
	if recent.Date < now {
		t.Fatalf("photo without a date = %d, want the current time", recent.Date)
	}

	video := domain.DocumentSpec{
		MimeType:   "video/mp4",
		Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrVideo, W: 320, H: 180, Duration: 2}},
		Date:       past,
	}
	doc, err := svc.CreateDocumentFromReaderWithThumb(ctx, bytes.NewReader([]byte("not really mp4")), video)
	if err != nil {
		t.Fatalf("CreateDocumentFromReaderWithThumb: %v", err)
	}
	if doc.Date != past {
		t.Fatalf("document date = %d, want the source date", doc.Date)
	}
	// 流式存入的视频也有兜底缩略图；不带缩略图的流式入口（安装包）保持原样。
	if len(doc.Thumbs) != 1 || thumbnailer.calls != 1 {
		t.Fatalf("streamed video = %d thumbs after %d extractions, want one", len(doc.Thumbs), thumbnailer.calls)
	}
	plain, err := svc.CreateDocumentFromReader(ctx, bytes.NewReader([]byte("not really mp4, again")), domain.DocumentSpec{MimeType: "video/mp4"}, 0)
	if err != nil {
		t.Fatalf("CreateDocumentFromReader: %v", err)
	}
	if len(plain.Thumbs) != 0 || plain.Date < now {
		t.Fatalf("plain streamed document = %d thumbs date %d, want no thumb and the current time", len(plain.Thumbs), plain.Date)
	}
	fromBytes, err := svc.CreateDocumentFromBytes(ctx, []byte("file"), domain.DocumentSpec{MimeType: "application/pdf", Date: past})
	if err != nil {
		t.Fatalf("CreateDocumentFromBytes: %v", err)
	}
	if fromBytes.Date != past {
		t.Fatalf("document from bytes date = %d, want the source date", fromBytes.Date)
	}
}
