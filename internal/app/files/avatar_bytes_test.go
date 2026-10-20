package files

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"telesrv/internal/domain"
	"testing"
)

func TestAvatarByteEntrypoints(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(media, blobs, 2, WithVideoThumbnailer(nil))
	t.Run("static uses avatar renditions", func(t *testing.T) {
		photo, err := svc.CreateAvatarFromBytes(ctx, testJPEG(t, 640, 480))
		if err != nil {
			t.Fatal(err)
		}
		for _, typ := range []string{"s", "a", "c"} {
			blob, ok, err := media.GetFileBlob(ctx, fmt.Sprintf("photo:%d:%s", photo.ID, typ))
			if err != nil || !ok {
				t.Fatalf("%s blob found=%v err=%v", typ, ok, err)
			}
			data, err := blobs.Get(ctx, blob.ObjectKey)
			if err != nil {
				t.Fatal(err)
			}
			cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			limit := 640
			if typ == "s" {
				limit = 150
			}
			if typ == "a" {
				limit = 160
			}
			if cfg.Width > limit || cfg.Height > limit {
				t.Fatalf("%s dimensions = %dx%d", typ, cfg.Width, cfg.Height)
			}
		}
	})
	t.Run("video over 512KB preserves all bytes across parts", func(t *testing.T) {
		data := bytes.Repeat([]byte("avatar video"), MaxUploadPartBytes/6)
		photo, err := svc.CreateAvatarVideoFromBytes(ctx, data, 0.25)
		if err != nil {
			t.Fatal(err)
		}
		var video domain.PhotoSize
		for _, size := range photo.Sizes {
			if size.Kind == domain.PhotoSizeKindVideo {
				video = size
			}
		}
		if video.Size != len(data) || video.VideoStartTs != 0.25 {
			t.Fatalf("video size = %+v", video)
		}
		blob, found, err := media.GetFileBlob(ctx, fmt.Sprintf("photo:%d:u", photo.ID))
		if err != nil || !found {
			t.Fatalf("video blob found=%v err=%v", found, err)
		}
		got, err := blobs.Get(ctx, blob.ObjectKey)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("video bytes differ: %v", err)
		}
	})
}
