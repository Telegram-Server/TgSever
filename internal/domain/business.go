package domain

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	MaxBusinessWorkHourIntervals = 28
	MaxBusinessRecipientUsers    = 100
	MaxBusinessChatLinks         = 100
	MaxBusinessChatLinkMessage   = MaxMessageTextLength
	MaxBusinessChatLinkTitle     = 64
	MaxQuickReplies              = 100
	MaxQuickReplyMessages        = 20
	MaxQuickReplyShortcutLength  = 32
	BusinessAwayCooldownSeconds  = 24 * 60 * 60
)

var (
	ErrBusinessProfileInvalid   = errors.New("business profile invalid")
	ErrBusinessChatLinkInvalid  = errors.New("business chat link invalid")
	ErrBusinessChatLinkNotFound = errors.New("business chat link not found")
	ErrBusinessChatLinksTooMuch = errors.New("business chat links too much")
	ErrBusinessRecipientsEmpty  = errors.New("business recipients empty")
	ErrBotBusinessMissing       = errors.New("bot business missing")
	ErrBotNotConnectedYet       = errors.New("bot not connected yet")
	ErrBotAlreadyDisabled       = errors.New("bot already disabled")
	ErrShortcutInvalid          = errors.New("quick reply shortcut invalid")
	ErrShortcutOccupied         = errors.New("quick reply shortcut occupied")
	ErrQuickRepliesTooMuch      = errors.New("quick replies too much")
)

type BusinessProfile struct {
	UserID        int64
	WorkHours     *BusinessWorkHours
	Location      *BusinessLocation
	Intro         *BusinessIntro
	Greeting      *BusinessGreetingMessage
	Away          *BusinessAwayMessage
	UpdatedAtUnix int64
}

type BusinessWeeklyOpen struct {
	StartMinute int
	EndMinute   int
}

type BusinessWorkHours struct {
	TimezoneID string
	WeeklyOpen []BusinessWeeklyOpen
	OpenNow    bool
}

type BusinessLocation struct {
	Address string
	Geo     *GeoPoint
}

type GeoPoint struct {
	Lat  float64
	Long float64
}

type BusinessIntro struct {
	Title             string
	Description       string
	StickerDocumentID int64
}

type BusinessRecipients struct {
	ExistingChats   bool
	NewChats        bool
	Contacts        bool
	NonContacts     bool
	ExcludeSelected bool
	Users           []int64
}

type BusinessBotRights struct {
	Reply                   bool
	ReadMessages            bool
	DeleteSentMessages      bool
	DeleteReceivedMessages  bool
	EditName                bool
	EditBio                 bool
	EditProfilePhoto        bool
	EditUsername            bool
	ViewGifts               bool
	SellGifts               bool
	ChangeGiftSettings      bool
	TransferAndUpgradeGifts bool
	TransferStars           bool
	ManageStories           bool
}

type BusinessBotRecipients struct {
	ExistingChats   bool
	NewChats        bool
	Contacts        bool
	NonContacts     bool
	ExcludeSelected bool
	Users           []int64
	ExcludeUsers    []int64
}

type ConnectedBusinessBot struct {
	ConnectionID  string
	Disabled      bool
	OwnerUserID   int64
	BotUserID     int64
	Recipients    BusinessBotRecipients
	Rights        BusinessBotRights
	CreatedAtUnix int64
	UpdatedAtUnix int64
}

type ConnectedBusinessBotPeerState struct {
	OwnerUserID   int64
	PeerUserID    int64
	Paused        bool
	Disabled      bool
	UpdatedAtUnix int64
}

// NewBusinessConnectionID creates an opaque, unguessable delegation identifier.
func NewBusinessConnectionID() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func (r BusinessBotRights) Allows(right string) bool {
	switch right {
	case "":
		return true
	case "can_reply":
		return r.Reply
	case "can_read_messages":
		return r.ReadMessages
	case "can_delete_sent_messages":
		return r.DeleteSentMessages
	case "can_delete_all_messages":
		return r.DeleteReceivedMessages
	case "can_edit_name":
		return r.EditName
	case "can_edit_bio":
		return r.EditBio
	case "can_edit_profile_photo":
		return r.EditProfilePhoto
	case "can_edit_username":
		return r.EditUsername
	case "can_change_gift_settings":
		return r.ChangeGiftSettings
	case "can_view_gifts_and_stars":
		return r.ViewGifts
	case "can_convert_gifts_to_stars":
		return r.SellGifts
	case "can_transfer_and_upgrade_gifts":
		return r.TransferAndUpgradeGifts
	case "can_transfer_stars":
		return r.TransferStars
	case "can_manage_stories":
		return r.ManageStories
	default:
		return false
	}
}

type BusinessGreetingMessage struct {
	ShortcutID     int
	Recipients     BusinessRecipients
	NoActivityDays int
}

type BusinessAwayScheduleKind string

const (
	BusinessAwayScheduleAlways           BusinessAwayScheduleKind = "always"
	BusinessAwayScheduleOutsideWorkHours BusinessAwayScheduleKind = "outside_work_hours"
	BusinessAwayScheduleCustom           BusinessAwayScheduleKind = "custom"
)

type BusinessAwaySchedule struct {
	Kind      BusinessAwayScheduleKind
	StartDate int
	EndDate   int
}

type BusinessAwayMessage struct {
	ShortcutID  int
	Schedule    BusinessAwaySchedule
	Recipients  BusinessRecipients
	OfflineOnly bool
}

type BusinessAutomationKind string

const (
	BusinessAutomationGreeting BusinessAutomationKind = "greeting"
	BusinessAutomationAway     BusinessAutomationKind = "away"
	BusinessAutomationAI       BusinessAutomationKind = "ai"
)

type BusinessAutomationDelivery struct {
	OwnerUserID      int64
	PeerUserID       int64
	Kind             BusinessAutomationKind
	TriggerMessageID int
	ShortcutID       int
	SentAt           int
}

type BusinessChatLinkInput struct {
	Message  string
	Entities []MessageEntity
	Title    string
}

type BusinessChatLink struct {
	OwnerUserID int64
	Slug        string
	Link        string
	Message     string
	Entities    []MessageEntity
	Title       string
	Views       int
	CreatedAt   int64
	UpdatedAt   int64
}

type QuickReply struct {
	OwnerUserID int64
	ID          int
	Shortcut    string
	TopMessage  int
	Count       int
	SortOrder   int
	CreatedAt   int64
	UpdatedAt   int64
}

type QuickReplyMessage struct {
	OwnerUserID int64
	ShortcutID  int
	ID          int
	RandomID    int64
	Date        int
	Message     string
	Entities    []MessageEntity
}

type QuickReplyList struct {
	OwnerUserID  int64
	QuickReplies []QuickReply
	Messages     []QuickReplyMessage
	Hash         int64
}

type QuickReplyMessages struct {
	OwnerUserID int64
	ShortcutID  int
	Messages    []QuickReplyMessage
	Count       int
	Hash        int64
}

type QuickReplyMutationKind string

const (
	QuickReplyMutationList    QuickReplyMutationKind = "list"
	QuickReplyMutationNew     QuickReplyMutationKind = "new"
	QuickReplyMutationDelete  QuickReplyMutationKind = "delete"
	QuickReplyMutationMessage QuickReplyMutationKind = "message"
	QuickReplyMutationIDs     QuickReplyMutationKind = "ids"
)

type QuickReplyMutation struct {
	Kind       QuickReplyMutationKind
	List       QuickReplyList
	QuickReply QuickReply
	ShortcutID int
	Message    QuickReplyMessage
	MessageIDs []int
	Date       int
	Pts        int
	PtsCount   int
}

func NormalizeBusinessChatLinkInput(in BusinessChatLinkInput) (BusinessChatLinkInput, error) {
	in.Message = strings.TrimSpace(in.Message)
	in.Title = strings.TrimSpace(in.Title)
	if in.Message == "" || utf8.RuneCountInString(in.Message) > MaxBusinessChatLinkMessage {
		return BusinessChatLinkInput{}, ErrBusinessChatLinkInvalid
	}
	if utf8.RuneCountInString(in.Title) > MaxBusinessChatLinkTitle {
		return BusinessChatLinkInput{}, ErrBusinessChatLinkInvalid
	}
	if len(in.Entities) > MaxMessageEntityCount {
		return BusinessChatLinkInput{}, ErrBusinessChatLinkInvalid
	}
	in.Entities = append([]MessageEntity(nil), in.Entities...)
	return in, nil
}

func NormalizeQuickReplyShortcut(shortcut string) (string, error) {
	shortcut = strings.TrimSpace(shortcut)
	if shortcut == "" || strings.ContainsAny(shortcut, "\r\n\t") || utf8.RuneCountInString(shortcut) > MaxQuickReplyShortcutLength {
		return "", ErrShortcutInvalid
	}
	return shortcut, nil
}

func BusinessBotRecipientsMatch(recipients BusinessBotRecipients, existingChat, isContact bool, userID int64) bool {
	selected := false
	if existingChat && recipients.ExistingChats {
		selected = true
	}
	if !existingChat && recipients.NewChats {
		selected = true
	}
	if isContact && recipients.Contacts {
		selected = true
	}
	if !isContact && recipients.NonContacts {
		selected = true
	}
	for _, id := range recipients.Users {
		if id == userID {
			selected = true
			break
		}
	}
	for _, id := range recipients.ExcludeUsers {
		if id == userID {
			if recipients.ExcludeSelected {
				selected = true
				break
			}
			return false
		}
	}
	if recipients.ExcludeSelected {
		return !selected
	}
	return selected
}
