package domain

import (
	"strings"
	"testing"
)

func TestDocumentIsStickerSetMaterialAcceptsUploadedFormats(t *testing.T) {
	tests := []struct {
		name     string
		doc      Document
		want     bool
		wantMime string
	}{
		{
			name:     "existing sticker",
			doc:      Document{ID: 1, AccessHash: 11, Size: MaxStickerMaterialDocumentSize + 1, Attributes: []DocumentAttribute{{Kind: DocAttrSticker}}},
			want:     true,
			wantMime: "",
		},
		{
			name:     "tgs mime",
			doc:      Document{ID: 2, AccessHash: 22, MimeType: "application/x-tgsticker", Size: 4096},
			want:     true,
			wantMime: "application/x-tgsticker",
		},
		{
			name:     "webp extension",
			doc:      Document{ID: 3, AccessHash: 33, MimeType: "application/octet-stream", Size: 4096, Attributes: []DocumentAttribute{{Kind: DocAttrFilename, FileName: "sticker.WEBP"}}},
			want:     true,
			wantMime: "image/webp",
		},
		{
			name:     "png mime",
			doc:      Document{ID: 34, AccessHash: 334, MimeType: "image/png", Size: 4096},
			want:     true,
			wantMime: "image/png",
		},
		{
			name:     "png extension",
			doc:      Document{ID: 35, AccessHash: 335, MimeType: "application/octet-stream", Size: 4096, Attributes: []DocumentAttribute{{Kind: DocAttrFilename, FileName: "mask.PNG"}}},
			want:     true,
			wantMime: "image/png",
		},
		{
			name:     "lottie json mime",
			doc:      Document{ID: 31, AccessHash: 331, MimeType: "application/lottie+json", Size: 4096},
			want:     true,
			wantMime: "application/json",
		},
		{
			name:     "lottie json extension",
			doc:      Document{ID: 32, AccessHash: 332, MimeType: "application/octet-stream", Size: 4096, Attributes: []DocumentAttribute{{Kind: DocAttrFilename, FileName: "wave.JSON"}}},
			want:     true,
			wantMime: "application/json",
		},
		{
			name:     "mp4 mime",
			doc:      Document{ID: 4, AccessHash: 44, MimeType: "video/mp4", Size: 4096, Attributes: []DocumentAttribute{{Kind: DocAttrVideo, W: 512, H: 512, Duration: 1}}},
			want:     true,
			wantMime: "video/mp4",
		},
		{
			name:     "mp4 without video attribute",
			doc:      Document{ID: 40, AccessHash: 440, MimeType: "video/mp4", Size: 4096},
			wantMime: "video/mp4",
		},
		{
			name:     "arbitrary file",
			doc:      Document{ID: 5, AccessHash: 55, MimeType: "application/pdf", Size: 4096},
			wantMime: "application/pdf",
		},
		{
			name:     "oversized upload",
			doc:      Document{ID: 6, AccessHash: 66, MimeType: "image/webp", Size: MaxStickerMaterialDocumentSize + 1},
			wantMime: "image/webp",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.doc.IsStickerSetMaterial(); got != tt.want {
				t.Fatalf("IsStickerSetMaterial() = %v, want %v", got, tt.want)
			}
			if got := tt.doc.StickerSetMaterialMime(); got != tt.wantMime {
				t.Fatalf("StickerSetMaterialMime() = %q, want %q", got, tt.wantMime)
			}
		})
	}
}

func TestCloneDocumentIsolatesStickerMaskAndFileMetadata(t *testing.T) {
	source := &Document{
		FileReference: []byte{1},
		Attributes: []DocumentAttribute{{
			Kind:         DocAttrSticker,
			MaskPosition: &StickerMaskPosition{Point: "eyes", XShift: 0.25, Scale: 1},
			Waveform:     []byte{2},
		}},
		Thumbs: []PhotoSize{{Bytes: []byte{3}, Sizes: []int{4}, BackgroundColors: []int{5}}},
	}
	copy := CloneDocumentPtr(source)
	copy.FileReference[0] = 11
	copy.Attributes[0].MaskPosition.XShift = 2
	copy.Attributes[0].Waveform[0] = 12
	copy.Thumbs[0].Bytes[0] = 13
	copy.Thumbs[0].Sizes[0] = 14
	copy.Thumbs[0].BackgroundColors[0] = 15
	if source.FileReference[0] != 1 || source.Attributes[0].MaskPosition.XShift != 0.25 ||
		source.Attributes[0].Waveform[0] != 2 || source.Thumbs[0].Bytes[0] != 3 ||
		source.Thumbs[0].Sizes[0] != 4 || source.Thumbs[0].BackgroundColors[0] != 5 {
		t.Fatalf("clone mutation changed the stored document: %+v", source)
	}
}

func TestClonePassportDataPreservesAuthorizedEncryptedSnapshot(t *testing.T) {
	element := PassportElement{
		ValueTL:   []byte{1},
		Files:     []PassportFile{{Secret: []byte{2}}},
		FrontSide: &PassportFile{Secret: []byte{3}},
	}
	source := PassportUserData{
		Elements: []PassportElement{element},
		Authorizations: []PassportAuthorization{{
			Elements:        []PassportElement{element},
			CredentialsData: []byte{6},
			Errors:          []PassportError{{FileHashes: []string{"original"}}},
		}},
	}
	copy := ClonePassportUserData(source)
	copy.Elements[0].ValueTL[0] = 11
	copy.Elements[0].Files[0].Secret[0] = 12
	copy.Authorizations[0].Elements[0].FrontSide.Secret[0] = 13
	copy.Authorizations[0].CredentialsData[0] = 16
	copy.Authorizations[0].Errors[0].FileHashes[0] = "changed"
	if source.Elements[0].ValueTL[0] != 1 || source.Elements[0].Files[0].Secret[0] != 2 ||
		source.Authorizations[0].Elements[0].FrontSide.Secret[0] != 3 ||
		source.Authorizations[0].CredentialsData[0] != 6 ||
		source.Authorizations[0].Errors[0].FileHashes[0] != "original" {
		t.Fatal("viewer mutation changed the stored Passport authorization")
	}
}

func TestCloneRichMessageIsolatesEmbeddedMedia(t *testing.T) {
	source := &MessageRichMessage{
		Blocks:           []byte{1},
		BotAPIProjection: []byte(`{"text":"original"}`),
		Photos:           []Photo{{FileReference: []byte{2}, Sizes: []PhotoSize{{Bytes: []byte{3}}}}},
		Documents:        []Document{{Attributes: []DocumentAttribute{{MaskPosition: &StickerMaskPosition{Point: "eyes", Scale: 1}}}}},
	}
	copy := CloneRichMessage(source)
	copy.Blocks[0] = 11
	copy.BotAPIProjection[0] = '['
	copy.Photos[0].FileReference[0] = 12
	copy.Photos[0].Sizes[0].Bytes[0] = 13
	copy.Documents[0].Attributes[0].MaskPosition.Scale = 2
	if source.Blocks[0] != 1 || source.BotAPIProjection[0] != '{' ||
		source.Photos[0].FileReference[0] != 2 || source.Photos[0].Sizes[0].Bytes[0] != 3 ||
		source.Documents[0].Attributes[0].MaskPosition.Scale != 1 {
		t.Fatal("rich-message view mutation changed the stored media snapshot")
	}
}

func TestInvoicePrivatePayloadAndPublicStartParameterHaveSeparateBudgets(t *testing.T) {
	invoice := BotInvoice{BotUserID: 1, ChatID: 2, MessageID: 3, Amount: 1, Currency: PremiumCurrencyStars}
	for _, size := range []int{0, 1, 64, 128, 129} {
		invoice.Payload = strings.Repeat("x", size)
		if invoice.Valid() != (size > 0 && size <= 128) {
			t.Errorf("private payload of %d bytes has wrong validity", size)
		}
	}
	media := Invoice{Title: "Product", Description: "Description", AmountStars: 1}
	for _, size := range []int{0, 64, 65, 128} {
		media.StartParam = strings.Repeat("x", size)
		if media.Valid() != (size <= 64) {
			t.Errorf("public start parameter of %d bytes has wrong validity", size)
		}
	}
}
