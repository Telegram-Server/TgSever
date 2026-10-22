package files

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"image"
	"io"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"telesrv/internal/domain"
)

func stickerSetCapacity(kind domain.StickerSetKind, botID int64) int {
	if botID != 0 && kind == domain.StickerSetKindEmoji {
		return 200
	}
	return domain.MaxStickerSetItems
}
func validateCreatedStickerSetName(name string, botID int64) error {
	if botID == 0 {
		return validateStickerSetShortName(name)
	}
	if len(name) < 1 || len(name) > 64 || name[0] < 'a' || name[0] > 'z' || strings.Contains(name, "__") {
		return domain.ErrStickerSetShortNameInvalid
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return domain.ErrStickerSetShortNameInvalid
		}
	}
	return nil
}
func applyStickerInputMetadata(doc domain.Document, item domain.StickerSetItemInput) domain.Document {
	for i := range doc.Attributes {
		if doc.Attributes[i].Kind == domain.DocAttrSticker || doc.Attributes[i].Kind == domain.DocAttrCustomEmoji {
			doc.Attributes[i].MaskPosition = item.MaskPosition
		}
	}
	return doc
}
func stickerInputIndexes(packs []domain.StickerPack, keywords []domain.StickerKeyword, id int64, item domain.StickerSetItemInput) ([]domain.StickerPack, []domain.StickerKeyword) {
	emojis := item.EmojiList
	if len(emojis) == 0 {
		emojis = []string{item.Emoji}
	}
	for _, emoji := range emojis {
		packs = addDocumentToStickerPacks(packs, emoji, id)
	}
	kw := parseStickerKeywords(id, item.Keywords)
	if item.KeywordsList != nil {
		kw.Keywords = append([]string(nil), item.KeywordsList...)
	}
	return packs, upsertStickerKeywords(keywords, kw)
}
func validateBotStickerMetadata(item domain.BotAPIInputSticker, kind domain.StickerSetKind) error {
	if len(item.EmojiList) < 1 || len(item.EmojiList) > 20 {
		return domain.ErrStickerSetEmojiInvalid
	}
	for _, e := range item.EmojiList {
		if err := validateStickerEmoji(e); err != nil {
			return err
		}
	}
	if err := validateBotStickerKeywords(item.Keywords, kind); err != nil {
		return err
	}
	if kind != domain.StickerSetKindMasks && item.MaskPosition != nil {
		return domain.ErrStickerSetTypeInvalid
	}
	return validateBotMaskPosition(item.MaskPosition)
}
func validateBotStickerKeywords(keywords []string, kind domain.StickerSetKind) error {
	if kind == domain.StickerSetKindMasks && len(keywords) > 0 {
		return domain.ErrStickerSetTypeInvalid
	}
	if len(keywords) > 20 {
		return domain.ErrStickerSetFileInvalid
	}
	n := 0
	for _, k := range keywords {
		n += utf8.RuneCountInString(k)
		if k == "" {
			return domain.ErrStickerSetFileInvalid
		}
	}
	if n > 64 {
		return domain.ErrStickerSetFileInvalid
	}
	return nil
}
func validateBotMaskPosition(p *domain.StickerMaskPosition) error {
	if p == nil {
		return nil
	}
	if p.Point != "forehead" && p.Point != "eyes" && p.Point != "mouth" && p.Point != "chin" || p.Scale <= 0 || math.IsNaN(p.Scale) || math.IsInf(p.Scale, 0) || math.IsNaN(p.XShift) || math.IsInf(p.XShift, 0) || math.IsNaN(p.YShift) || math.IsInf(p.YShift, 0) {
		return domain.ErrStickerSetFileInvalid
	}
	return nil
}

// validateBotStickerBytes inspects bytes, never trusts MIME, extension or caller dimensions.
func validateBotStickerBytes(ctx context.Context, data []byte, format string, kind domain.StickerSetKind, thumbnail bool) (domain.DocumentSpec, error) {
	bad := func() (domain.DocumentSpec, error) { return domain.DocumentSpec{}, domain.ErrStickerSetFileInvalid }
	if len(data) == 0 || len(data) > 512<<10 {
		return bad()
	}
	var w, h int
	var mime string
	var attrs []domain.DocumentAttribute
	switch format {
	case "static":
		cfg, f, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || f != "png" && f != "webp" || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 512 || cfg.Height > 512 {
			return bad()
		}
		if _, _, err = image.Decode(bytes.NewReader(data)); err != nil {
			return bad()
		}
		w, h = cfg.Width, cfg.Height
		mime = "image/" + f
		if thumbnail && len(data) > 128<<10 {
			return bad()
		}
	case "animated":
		limit := 64 << 10
		if thumbnail {
			limit = 32 << 10
		}
		if len(data) > limit {
			return bad()
		}
		z, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return bad()
		}
		raw, err := io.ReadAll(io.LimitReader(z, domain.MaxStickerMaterialDocumentSize+1))
		z.Close()
		if err != nil || int64(len(raw)) > domain.MaxStickerMaterialDocumentSize {
			return bad()
		}
		var a struct {
			W  int     `json:"w"`
			H  int     `json:"h"`
			FR float64 `json:"fr"`
			IP float64 `json:"ip"`
			OP float64 `json:"op"`
		}
		if json.Unmarshal(raw, &a) != nil || a.FR != 60 || a.OP <= a.IP || (a.OP-a.IP)/a.FR > 3 {
			return bad()
		}
		var animation any
		if json.Unmarshal(raw, &animation) != nil || !validBotStickerAnimation(animation) {
			return bad()
		}
		w, h = a.W, a.H
		mime = stickerMaterialMimeTGS
	case "video":
		limit := 256 << 10
		if thumbnail {
			limit = 32 << 10
		}
		if len(data) > limit || !botStickerWebM(data) {
			return bad()
		}
		probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(probeCtx, "ffprobe", "-v", "error", "-show_entries", "stream=codec_type,codec_name,width,height,duration,avg_frame_rate:format=duration,format_name:packet=pts_time,duration_time", "-of", "json", "pipe:0")
		cmd.Stdin = bytes.NewReader(data)
		raw, err := cmd.Output()
		if err != nil {
			return domain.DocumentSpec{}, err
		}
		var p struct {
			Streams []struct {
				CodecType string `json:"codec_type"`
				CodecName string `json:"codec_name"`
				Width     int    `json:"width"`
				Height    int    `json:"height"`
				Rate      string `json:"avg_frame_rate"`
			} `json:"streams"`
			Format struct {
				Name string `json:"format_name"`
			} `json:"format"`
			Packets []struct {
				PTS      string `json:"pts_time"`
				Duration string `json:"duration_time"`
			} `json:"packets"`
		}
		if json.Unmarshal(raw, &p) != nil || len(p.Streams) != 1 || len(p.Packets) == 0 || !strings.Contains(p.Format.Name, "webm") {
			return bad()
		}
		v := p.Streams[0]
		rate := strings.Split(v.Rate, "/")
		fps := 0.0
		if len(rate) == 2 {
			num, _ := strconv.ParseFloat(rate[0], 64)
			den, _ := strconv.ParseFloat(rate[1], 64)
			if den > 0 {
				fps = num / den
			}
		}
		if v.CodecType != "video" || v.CodecName != "vp9" || fps <= 0 || fps > 30 || math.IsNaN(fps) || math.IsInf(fps, 0) {
			return bad()
		}
		start, end := math.Inf(1), math.Inf(-1)
		for _, packet := range p.Packets {
			pts, e := strconv.ParseFloat(packet.PTS, 64)
			if e != nil || math.IsNaN(pts) || math.IsInf(pts, 0) {
				return bad()
			}
			d, e := strconv.ParseFloat(packet.Duration, 64)
			if e != nil || d <= 0 {
				d = 1 / fps
			}
			start = math.Min(start, pts)
			end = math.Max(end, pts+d)
		}
		duration := end - start
		if duration <= 0 || duration > 3.001 {
			return bad()
		}
		w, h = v.Width, v.Height
		mime = stickerMaterialMimeWebM
		attrs = append(attrs, domain.DocumentAttribute{Kind: domain.DocAttrVideo, W: w, H: h, Duration: duration, NoSound: true, VideoCodec: "vp9"})
	default:
		return bad()
	}
	valid := w > 0 && h > 0 && w <= 512 && h <= 512 && (w == 512 || h == 512)
	if kind == domain.StickerSetKindEmoji || thumbnail {
		valid = w == 100 && h == 100
	}
	if kind == "" {
		valid = valid || w == 100 && h == 100
	}
	if format == "animated" && !thumbnail {
		valid = w == 512 && h == 512
	}
	if !valid {
		return bad()
	}
	attrs = append(attrs, domain.DocumentAttribute{Kind: domain.DocAttrImageSize, W: w, H: h})
	return domain.DocumentSpec{MimeType: mime, Attributes: attrs, ForceFile: true}, nil
}

func (s *Service) BotStickerFile(ctx context.Context, file domain.BotAPIFileInput, format string, kind domain.StickerSetKind, thumbnail bool) (domain.Document, error) {
	if file.RemoteURL != "" {
		if format != "static" || s.externalMedia == nil {
			return domain.Document{}, domain.ErrStickerSetFileInvalid
		}
		data, _, err := s.externalMedia.fetch(ctx, file.RemoteURL)
		if err != nil {
			return domain.Document{}, err
		}
		file.Bytes = data
	}
	if thumbnail && len(file.Bytes) == 0 && file.LocationKey != "" {
		blob, found, err := s.media.GetFileBlob(ctx, file.LocationKey)
		if err != nil {
			return domain.Document{}, err
		}
		if !found || blob.Size <= 0 || blob.Size > 512<<10 {
			return domain.Document{}, domain.ErrStickerSetFileInvalid
		}
		data, total, err := s.blobs.GetRange(ctx, blob.ObjectKey, 0, blob.Size)
		if err != nil {
			return domain.Document{}, err
		}
		if total != blob.Size || int64(len(data)) != blob.Size {
			return domain.Document{}, domain.ErrStickerSetFileInvalid
		}
		file.Bytes = data
	}
	if len(file.Bytes) > 0 {
		spec, err := validateBotStickerBytes(ctx, file.Bytes, format, kind, thumbnail)
		if err != nil {
			return domain.Document{}, err
		}
		return s.CreateDocumentFromBytes(ctx, file.Bytes, spec)
	}
	if !strings.HasPrefix(file.LocationKey, "doc:") {
		return domain.Document{}, domain.ErrStickerSetFileInvalid
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(file.LocationKey, "doc:"), 10, 64)
	if err != nil {
		return domain.Document{}, domain.ErrStickerSetFileInvalid
	}
	docs, err := s.media.GetDocuments(ctx, []int64{id})
	if err != nil {
		return domain.Document{}, err
	}
	if len(docs) != 1 {
		return domain.Document{}, domain.ErrStickerSetFileInvalid
	}
	data, ok := s.readStickerMaterialBlob(ctx, docs[0])
	if !ok {
		return domain.Document{}, domain.ErrStickerSetFileInvalid
	}
	_, err = validateBotStickerBytes(ctx, data, format, kind, thumbnail)
	return docs[0], err
}

func validBotStickerAnimation(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		if d, ok := v["ddd"].(float64); ok && d != 0 {
			return false
		}
		if _, ok := v["tm"]; ok {
			return false
		}
		if _, ok := v["p"].(string); ok {
			return false
		} // external or embedded raster asset
		if x, ok := v["x"].(string); ok && x != "" {
			return false
		} // After Effects expression
		if masks, ok := v["masksProperties"].([]any); ok && len(masks) > 0 {
			return false
		}
		if stretch, ok := v["sr"].(float64); ok && stretch != 1 {
			return false
		}
		if ty, ok := v["ty"].(float64); ok && (ty == 1 || ty == 2 || ty == 5) {
			return false
		}
		if ty, ok := v["ty"].(string); ok && (ty == "mm" || ty == "sr" || ty == "gs" || ty == "rp") {
			return false
		}
		for _, child := range v {
			if !validBotStickerAnimation(child) {
				return false
			}
		}
	case []any:
		for _, child := range v {
			if !validBotStickerAnimation(child) {
				return false
			}
		}
	}
	return true
}

func updateBotStickerSetFormats(set *domain.StickerSet, docs []domain.Document) {
	if set.CreatorBotID == 0 {
		return
	}
	set.Animated = false
	set.Videos = false
	for _, doc := range docs {
		set.Animated = set.Animated || doc.MimeType == stickerMaterialMimeTGS
		set.Videos = set.Videos || doc.MimeType == stickerMaterialMimeWebM
	}
}

func botStickerWebM(data []byte) bool {
	if len(data) < 5 || !bytes.Equal(data[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}) {
		return false
	}
	size, n, ok := botStickerEBMLInteger(data[4:], false)
	if !ok || size > uint64(len(data)-4-n) {
		return false
	}
	header := data[4+n : 4+n+int(size)]
	for len(header) > 0 {
		id, idBytes, ok := botStickerEBMLInteger(header, true)
		if !ok {
			return false
		}
		size, sizeBytes, ok := botStickerEBMLInteger(header[idBytes:], false)
		if !ok || size > uint64(len(header)-idBytes-sizeBytes) {
			return false
		}
		value := header[idBytes+sizeBytes : idBytes+sizeBytes+int(size)]
		if id == 0x4282 {
			return string(value) == "webm"
		}
		header = header[idBytes+sizeBytes+int(size):]
	}
	return false
}

func botStickerEBMLInteger(data []byte, identifier bool) (uint64, int, bool) {
	if len(data) == 0 || data[0] == 0 {
		return 0, 0, false
	}
	length, marker := 1, byte(0x80)
	for data[0]&marker == 0 {
		length++
		marker >>= 1
	}
	if length > len(data) {
		return 0, 0, false
	}
	value := uint64(data[0])
	if !identifier {
		value &= uint64(marker - 1)
	}
	for _, b := range data[1:length] {
		value = value<<8 | uint64(b)
	}
	return value, length, true
}
