package domain

// ChannelMessageAuthor identifies current membership metadata for a visible message.
type ChannelMessageAuthor struct {
	ChannelID int64
	UserID    int64
}
