package files

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"os/exec"
	"testing"

	"telesrv/internal/domain"
)

func TestBotStickerMaterialFormatAndDimensions(t *testing.T) {
	ctx := context.Background()
	pngData := func(w, h int) []byte {
		var b bytes.Buffer
		if err := png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, w, h))); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	tgsData := func(w, h, frames, fps int) []byte {
		a := map[string]any{"w": w, "h": h, "fr": fps, "ip": 0, "op": frames, "layers": []any{map[string]any{"ty": 4, "shapes": []any{map[string]any{"ty": "el"}}}}}
		var b bytes.Buffer
		z := gzip.NewWriter(&b)
		if err := json.NewEncoder(z).Encode(a); err != nil {
			t.Fatal(err)
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	for _, tc := range []struct {
		name      string
		data      []byte
		format    string
		kind      domain.StickerSetKind
		thumbnail bool
		valid     bool
	}{
		{"regular PNG", pngData(512, 256), "static", domain.StickerSetKindStickers, false, true},
		{"emoji PNG", pngData(100, 100), "static", domain.StickerSetKindEmoji, false, true},
		{"regular wrong size", pngData(100, 100), "static", domain.StickerSetKindStickers, false, false},
		{"emoji wrong size", pngData(512, 512), "static", domain.StickerSetKindEmoji, false, false},
		{"thumbnail", pngData(100, 100), "static", domain.StickerSetKindMasks, true, true},
		{"thumbnail wrong size", pngData(512, 512), "static", domain.StickerSetKindMasks, true, false},
		{"mime cannot turn PNG into animation", pngData(512, 512), "animated", domain.StickerSetKindStickers, false, false},
		{"TGS", tgsData(512, 512, 180, 60), "animated", domain.StickerSetKindStickers, false, true},
		{"emoji TGS canvas", tgsData(512, 512, 180, 60), "animated", domain.StickerSetKindEmoji, false, true},
		{"TGS duration", tgsData(512, 512, 181, 60), "animated", domain.StickerSetKindStickers, false, false},
		{"TGS fps", tgsData(512, 512, 90, 30), "animated", domain.StickerSetKindStickers, false, false},
		{"TGS dimensions", tgsData(511, 512, 180, 60), "animated", domain.StickerSetKindStickers, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateBotStickerBytes(ctx, tc.data, tc.format, tc.kind, tc.thumbnail)
			if (err == nil) != tc.valid {
				t.Fatalf("validation error = %v, valid=%v", err, tc.valid)
			}
		})
	}
}

func TestBotStickerVideoChecksRealStreams(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	if _, err = exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe unavailable")
	}
	for _, tc := range []struct {
		name, size, codec, seconds, fps string
		audio, valid                    bool
	}{
		{name: "VP9", size: "512x256", codec: "libvpx-vp9", seconds: "1", fps: "30", valid: true},
		{name: "VP8 rejected", size: "512x256", codec: "libvpx", seconds: "1", fps: "30"},
		{name: "audio rejected", size: "512x256", codec: "libvpx-vp9", seconds: "1", fps: "30", audio: true},
		{name: "duration rejected", size: "512x256", codec: "libvpx-vp9", seconds: "3.2", fps: "30"},
		{name: "frame rate rejected", size: "512x256", codec: "libvpx-vp9", seconds: "1", fps: "60"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=green:s=" + tc.size + ":r=" + tc.fps}
			if tc.audio {
				args = append(args, "-f", "lavfi", "-i", "anullsrc=channel_layout=mono:sample_rate=48000")
			}
			args = append(args, "-t", tc.seconds, "-c:v", tc.codec, "-b:v", "40k")
			if tc.audio {
				args = append(args, "-c:a", "libopus")
			}
			args = append(args, "-f", "webm", "pipe:1")
			data, err := exec.Command(ffmpeg, args...).Output()
			if err != nil {
				t.Fatalf("encode fixture: %v", err)
			}
			_, err = validateBotStickerBytes(context.Background(), data, "video", domain.StickerSetKindStickers, false)
			if (err == nil) != tc.valid {
				t.Fatalf("stream validation = %v, valid=%v", err, tc.valid)
			}
		})
	}
}
