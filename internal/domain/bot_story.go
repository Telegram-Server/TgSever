package domain

// BotStoryOwner binds a story to the bot and business connection that published it.
type BotStoryOwner struct {
	BotID                int64
	BusinessConnectionID string
}

// BotStoryContent only accepts new uploaded bytes, never a reusable file ID or URL.
type BotStoryContent struct {
	Type                string
	File                BotAPIFileInput
	Duration            float64
	CoverFrameTimestamp float64
	IsAnimation         bool
}

type BotStoryInput struct {
	BusinessConnectionID string
	StoryID              int
	FromChatID           int64
	FromStoryID          int
	Content              BotStoryContent
	ActivePeriod         int
	Caption              string
	Entities             []MessageEntity
	Areas                []StoryMediaArea
	PostToChatPage       bool
	ProtectContent       bool
}
