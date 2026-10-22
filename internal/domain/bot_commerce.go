package domain

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

const StarSubscriptionPeriod = 2592000

var ErrCommerceInvalid = errors.New("commerce request invalid")
var ErrCommerceNotFound = errors.New("commerce object not found")
var ErrPaidMediaLocked = errors.New("paid media locked")

// InvoiceLink is a reusable offer, never a fabricated message identifier.
// A separate checkout invoice is created for each payment form.
type InvoiceLink struct {
	Slug                 string
	BotID                int64
	OwnerID              int64
	BusinessConnectionID string
	Title                string
	Description          string
	Payload              string
	Amount               int64
	SubscriptionPeriod   int
	ChannelID            int64
	InviteHash           string
	Date                 int
}

func (v InvoiceLink) Valid() bool {
	return v.Slug != "" && v.BotID > 0 && v.Amount > 0 &&
		utf8.ValidString(v.Title) && utf8.RuneCountInString(v.Title) >= 1 && utf8.RuneCountInString(v.Title) <= 32 &&
		utf8.ValidString(v.Description) && utf8.RuneCountInString(v.Description) >= 1 && utf8.RuneCountInString(v.Description) <= 255 &&
		len(v.Payload) >= 1 && len(v.Payload) <= 128 && (v.SubscriptionPeriod == 0 || (v.SubscriptionPeriod == StarSubscriptionPeriod && v.Amount <= 10000))
}

type StarSubscription struct {
	ID           string
	Slug         string
	BotID        int64
	UserID       int64
	ChannelID    int64
	ChargeID     string
	Period       int
	Amount       int64
	Until        int
	Canceled     bool
	UserCanceled bool
	Active       bool
}

// PaidMedia keeps the real file capabilities out of the stored message. Only a
// purchased/viewer-specific projection may populate Unlocked.
type PaidMedia struct {
	ID               string
	BotID            int64
	OwnerID          int64
	ChatID           int64
	ChannelID        int64
	RevenueChannelID int64
	Price            int64
	Payload          string
	Items            []*MessageMedia
	Unlocked         bool
}
type PaidMediaInput struct {
	Kind string
	File BotAPIFileInput
}
type SendPaidMediaRequest struct {
	BotID                 int64
	ChatID                int64
	Price                 int64
	Payload               string
	Media                 []PaidMediaInput
	Caption               string
	Entities              []MessageEntity
	ReplyMarkup           *MessageReplyMarkup
	Options               BotAPIMessageOptions
	ShowCaptionAboveMedia bool
	Silent                bool
}
type BotStarTransaction struct {
	ID                 string
	Amount             int64
	Nanos              int32
	Date               int
	Peer               Peer
	Reason             StarsTransactionReason
	Payload            string
	SubscriptionPeriod int
}

// ClonePaidMedia isolates mutable file metadata between stored and viewer snapshots.
func ClonePaidMedia(v *PaidMedia) *PaidMedia {
	if v == nil {
		return nil
	}
	out := *v
	out.Items = make([]*MessageMedia, len(v.Items))
	for i, item := range v.Items {
		if item == nil {
			continue
		}
		clone := *item
		clone.Photo = ClonePhotoPtr(item.Photo)
		clone.Document = CloneDocumentPtr(item.Document)
		clone.LivePhotoVideo = CloneDocumentPtr(item.LivePhotoVideo)
		clone.VideoCover = ClonePhotoPtr(item.VideoCover)
		out.Items[i] = &clone
	}
	return &out
}
func PaidMediaFileKeys(v PaidMedia) []string {
	var keys []string
	for _, item := range v.Items {
		if item == nil {
			continue
		}
		if item.Photo != nil {
			for _, size := range item.Photo.Sizes {
				keys = append(keys, fmt.Sprintf("photo:%d:%s", item.Photo.ID, size.Type))
			}
		}
		if item.VideoCover != nil {
			for _, size := range item.VideoCover.Sizes {
				keys = append(keys, fmt.Sprintf("photo:%d:%s", item.VideoCover.ID, size.Type))
			}
		}
		if item.Document != nil {
			keys = append(keys, fmt.Sprintf("doc:%d", item.Document.ID))
			for _, size := range item.Document.Thumbs {
				keys = append(keys, fmt.Sprintf("doc:%d:%s", item.Document.ID, size.Type))
			}
		}
	}
	return keys
}
func PaidMediaPreview(v PaidMedia) PaidMedia {
	out := v
	out.Unlocked = false
	out.Payload = ""
	out.Items = make([]*MessageMedia, len(v.Items))
	for i, item := range v.Items {
		preview := &MessageMedia{}
		if item != nil {
			preview.Kind = item.Kind
			preview.Video = item.Video
			if item.Photo != nil {
				preview.Photo = &Photo{}
				for _, size := range item.Photo.Sizes {
					preview.Photo.Sizes = append(preview.Photo.Sizes, PhotoSize{Type: size.Type, W: size.W, H: size.H})
				}
			}
			if item.Document != nil {
				preview.Document = &Document{}
				for _, attr := range item.Document.Attributes {
					if attr.Kind == DocAttrVideo {
						preview.Document.Attributes = append(preview.Document.Attributes, DocumentAttribute{Kind: DocAttrVideo, W: attr.W, H: attr.H, Duration: attr.Duration})
					}
				}
			}
		}
		out.Items[i] = preview
	}
	return out
}
