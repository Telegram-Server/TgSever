package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// PrivateForumTopic belongs to a bot/user dialog, not a channel. ID is the
// bot's root message box ID; PeerTopicID is the user's corresponding root box.
type PrivateForumTopic struct {
	OwnerUserID int64
	PeerUserID  int64
	ID          int
	PeerTopicID int
	Name        string
	IconColor   int
	IconEmojiID int64
	Date        int
	Deleted     bool
}

type CreatePrivateForumTopicRequest struct {
	OwnerUserID      int64
	PeerUserID       int64
	Name             string
	IconColor        int
	IconEmojiID      int64
	RandomID         int64
	Date             int
	RecipientBlocked bool
}

type EditPrivateForumTopicRequest struct {
	OwnerUserID      int64
	PeerUserID       int64
	TopicID          int
	Name             *string
	IconEmojiID      *int64
	RandomID         int64
	Date             int
	RecipientBlocked bool
}

type PrivateForumTopicResult struct {
	Topic   PrivateForumTopic
	Message SendPrivateTextResult
}

const (
	MessageServiceActionTopicCreate MessageServiceActionKind = "topic_create"
	MessageServiceActionTopicEdit   MessageServiceActionKind = "topic_edit"
)

var ErrPrivateForumBlocked = errors.New("USER_IS_BLOCKED")

func ValidatePrivateForumTopicName(name string) error {
	if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > MaxChannelForumTopicTitleLength {
		return ErrMessageEmpty
	}
	return nil
}

func PrivateForumMessageTopic(msg Message) int {
	if msg.ReplyTo != nil && msg.ReplyTo.ForumTopic {
		return msg.ReplyTo.TopMessageID
	}
	return 0
}
