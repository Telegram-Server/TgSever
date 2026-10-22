package domain

// BotAPIOwnedGift contains only the viewer-authorized facts needed by OwnedGift.
// Public lists never carry business identifiers or private sender/text fields.
type BotAPIOwnedGift struct {
	Saved       SavedStarGift
	Gift        StarGift
	Sender      *User
	Publisher   *Channel
	Business    bool
	CanTransfer bool
	Now         int
	EntityUsers map[int64]User
}

type BotAPIOwnedGiftPage struct {
	Gifts      []BotAPIOwnedGift
	NextOffset string
	Count      int
}
