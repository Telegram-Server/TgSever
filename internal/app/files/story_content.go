package files

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"telesrv/internal/domain"
	"time"
)

// CreateStoryMedia validates the original upload, not a client-supplied MIME or dimensions.
func (s *Service) CreateStoryMedia(ctx context.Context, input domain.BotStoryContent) (*domain.MessageMedia, error) {
	invalid := errors.New("STORY_CONTENT_INVALID")
	if input.File.LocationKey != "" || input.File.RemoteURL != "" || len(input.File.Bytes) == 0 {
		return nil, invalid
	}
	if input.Type == "photo" {
		if len(input.File.Bytes) > 10<<20 {
			return nil, invalid
		}
		config, _, err := image.DecodeConfig(bytes.NewReader(input.File.Bytes))
		if err != nil || config.Width != 1080 || config.Height != 1920 {
			return nil, invalid
		}
		photo, err := s.CreatePhotoFromBytes(ctx, input.File.Bytes)
		if err != nil {
			return nil, err
		}
		return &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &photo}, nil
	}
	if input.Type != "video" || len(input.File.Bytes) > 30<<20 || !storyMP4FastStart(input.File.Bytes) || math.IsNaN(input.Duration) || math.IsInf(input.Duration, 0) || input.Duration < 0 || input.Duration > 60 || math.IsNaN(input.CoverFrameTimestamp) || math.IsInf(input.CoverFrameTimestamp, 0) || input.CoverFrameTimestamp < 0 {
		return nil, invalid
	}
	dir, err := os.MkdirTemp("", "telesrv-story-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "story.mp4")
	if err := os.WriteFile(path, input.File.Bytes, 0600); err != nil {
		return nil, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	probe, err := exec.CommandContext(probeCtx, "ffprobe", "-v", "error", "-skip_frame", "nokey", "-show_frames", "-show_streams", "-show_format", "-show_entries", "stream=codec_type,codec_name,width,height,duration:format=format_name,duration:frame=key_frame,best_effort_timestamp_time,media_type", "-of", "json", path).Output()
	if err != nil {
		return nil, err
	}
	var metadata struct {
		Streams []struct {
			Type     string `json:"codec_type"`
			Codec    string `json:"codec_name"`
			Width    int    `json:"width"`
			Height   int    `json:"height"`
			Duration string `json:"duration"`
		} `json:"streams"`
		Format struct {
			Name     string `json:"format_name"`
			Duration string `json:"duration"`
		} `json:"format"`
		Frames []struct {
			Type      string `json:"media_type"`
			Key       int    `json:"key_frame"`
			Timestamp string `json:"best_effort_timestamp_time"`
		} `json:"frames"`
	}
	if json.Unmarshal(probe, &metadata) != nil || !strings.Contains(metadata.Format.Name, "mp4") {
		return nil, invalid
	}
	duration, err := strconv.ParseFloat(metadata.Format.Duration, 64)
	if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 || duration > 60 || input.CoverFrameTimestamp >= duration {
		return nil, invalid
	}
	if input.Duration != 0 && math.Abs(input.Duration-duration) > 0.1 {
		return nil, invalid
	}
	videoCount := 0
	for _, stream := range metadata.Streams {
		if stream.Type == "audio" && input.IsAnimation {
			return nil, invalid
		}
		if stream.Type != "video" {
			continue
		}
		videoCount++
		if stream.Codec != "hevc" || stream.Width != 720 || stream.Height != 1280 {
			return nil, invalid
		}
	}
	if videoCount != 1 {
		return nil, invalid
	}
	previous := -1.0
	for _, frame := range metadata.Frames {
		if frame.Type != "video" || frame.Key != 1 {
			continue
		}
		timestamp, err := strconv.ParseFloat(frame.Timestamp, 64)
		if err != nil || math.IsNaN(timestamp) || math.IsInf(timestamp, 0) || timestamp < 0 || (previous < 0 && timestamp > 0.1) || (previous >= 0 && (timestamp < previous || timestamp-previous > 1.1)) {
			return nil, invalid
		}
		previous = timestamp
	}
	if previous < 0 || duration-previous > 1.1 {
		return nil, invalid
	}
	coverCtx, coverCancel := context.WithTimeout(ctx, 30*time.Second)
	defer coverCancel()
	coverBytes, err := exec.CommandContext(coverCtx, "ffmpeg", "-v", "error", "-nostdin", "-ss", strconv.FormatFloat(input.CoverFrameTimestamp, 'f', 6, 64), "-i", path, "-frames:v", "1", "-f", "image2pipe", "-vcodec", "mjpeg", "pipe:1").Output()
	if err != nil {
		return nil, err
	}
	cover, err := s.CreatePhotoFromBytes(ctx, coverBytes)
	if err != nil {
		return nil, err
	}
	if input.Duration > 0 {
		duration = input.Duration
	}
	document, err := s.CreateDocumentFromBytes(ctx, input.File.Bytes, domain.DocumentSpec{MimeType: "video/mp4", NosoundVideo: input.IsAnimation, Attributes: []domain.DocumentAttribute{
		{Kind: domain.DocAttrFilename, FileName: "story.mp4"},
		{Kind: domain.DocAttrVideo, W: 720, H: 1280, Duration: duration, SupportsStreaming: true, NoSound: input.IsAnimation, VideoCodec: "hevc"},
	}})
	if err != nil {
		return nil, err
	}
	return &domain.MessageMedia{Kind: domain.MessageMediaKindDocument, Document: &document, Video: true, VideoCover: &cover, VideoTimestamp: input.CoverFrameTimestamp}, nil
}

func storyMP4FastStart(data []byte) bool {
	moov, ftyp := false, false
	for offset := 0; offset+8 <= len(data); {
		size := uint64(binary.BigEndian.Uint32(data[offset : offset+4]))
		kind := string(data[offset+4 : offset+8])
		header := 8
		if size == 1 {
			if offset+16 > len(data) {
				return false
			}
			size, header = binary.BigEndian.Uint64(data[offset+8:offset+16]), 16
		} else if size == 0 {
			size = uint64(len(data) - offset)
		}
		if size < uint64(header) || size > uint64(len(data)-offset) {
			return false
		}
		switch kind {
		case "ftyp":
			ftyp = true
		case "moov":
			moov = true
		case "mdat":
			return ftyp && moov
		}
		offset += int(size)
	}
	return false
}
