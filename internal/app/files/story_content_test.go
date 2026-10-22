package files

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"telesrv/internal/domain"
	"testing"
)

func TestStoryMediaValidatesOriginalPhotoAndStoresBytes(t *testing.T) {
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(media, blobs, 2)
	photo := func(w, h int) domain.BotStoryContent {
		var b bytes.Buffer
		if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
			t.Fatal(err)
		}
		return domain.BotStoryContent{Type: "photo", File: domain.BotAPIFileInput{Bytes: b.Bytes()}}
	}
	if _, err := s.CreateStoryMedia(context.Background(), photo(720, 1280)); err == nil {
		t.Fatal("wrong original dimensions accepted")
	}
	if _, err := s.CreateStoryMedia(context.Background(), domain.BotStoryContent{Type: "photo", File: domain.BotAPIFileInput{Bytes: []byte("not-an-image")}}); err == nil {
		t.Fatal("corrupt image accepted")
	}
	result, err := s.CreateStoryMedia(context.Background(), photo(1080, 1920))
	if err != nil {
		t.Fatal(err)
	}
	if result.Photo == nil {
		t.Fatal("no stored photo")
	}
	blob, found, err := media.GetFileBlob(context.Background(), "photo:"+strconv.FormatInt(result.Photo.ID, 10)+":x")
	if err != nil || !found {
		t.Fatalf("photo blob = %+v, %v, %v", blob, found, err)
	}
}

func TestStoryVideoCoverUsesRequestedFrame(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " unavailable")
		}
	}
	path := filepath.Join(t.TempDir(), "story.mp4")
	command := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-f", "lavfi", "-i", "color=c=red:s=720x1280:r=5:d=1", "-f", "lavfi", "-i", "color=c=blue:s=720x1280:r=5:d=1", "-filter_complex", "[0:v][1:v]concat=n=2:v=1:a=0", "-c:v", "libx265", "-preset", "ultrafast", "-x265-params", "pools=1:keyint=5:min-keyint=5:scenecut=0", "-pix_fmt", "yuv420p", "-tag:v", "hvc1", "-movflags", "+faststart", path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create real HEVC fixture: %v: %s", err, output)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(media, blobs, 2, WithVideoThumbnailer(nil))
	result, err := s.CreateStoryMedia(context.Background(), domain.BotStoryContent{Type: "video", File: domain.BotAPIFileInput{Bytes: data}, Duration: 2, CoverFrameTimestamp: 1.2, IsAnimation: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.VideoCover == nil || result.Document == nil || result.VideoTimestamp != 1.2 {
		t.Fatalf("video = %+v", result)
	}
	chunk, found, err := s.GetFile(context.Background(), domain.FileDownloadRequest{LocationKey: "photo:" + strconv.FormatInt(result.VideoCover.ID, 10) + ":x", Limit: 1 << 20})
	if err != nil || !found {
		t.Fatalf("cover download: %v, %v", found, err)
	}
	cover, _, err := image.Decode(bytes.NewReader(chunk.Bytes))
	if err != nil {
		t.Fatal(err)
	}
	red, _, blue, _ := cover.At(cover.Bounds().Dx()/2, cover.Bounds().Dy()/2).RGBA()
	if blue <= red*2 {
		t.Fatalf("cover is not the blue second segment: r=%d b=%d", red, blue)
	}
}
