package domain

// StickerMaskPosition is shared by persisted document attributes and Bot API inputs.
type StickerMaskPosition struct {
	Point  string  `json:"point"`
	XShift float64 `json:"x_shift"`
	YShift float64 `json:"y_shift"`
	Scale  float64 `json:"scale"`
}

type BotAPIInputSticker struct {
	File         BotAPIFileInput
	Format       string
	EmojiList    []string
	Keywords     []string
	MaskPosition *StickerMaskPosition
}

type BotAPICreateStickerSet struct {
	UserID          int64
	Name            string
	Title           string
	Kind            StickerSetKind
	NeedsRepainting bool
	Stickers        []BotAPIInputSticker
}
