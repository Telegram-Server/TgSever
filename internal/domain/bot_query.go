package domain

// BotPreCheckoutQuery is the protocol-neutral payload of the MTProto
// updateBotPrecheckoutQuery.
//
// It asks the bot to confirm an order before any Stars move. The payer is held in
// the payment RPC for PreCheckoutTimeout; if the bot has not answered by then the
// payment is failed, which is what makes this a real gate and not a notification.
type BotPreCheckoutQuery struct {
	ID        int64 `json:"id"`
	BotUserID int64 `json:"bot_user_id"`
	UserID    int64 `json:"user_id"`
	// Currency and TotalAmount are what the payer is about to be charged. The bot
	// is expected to verify them against its own order state.
	Currency    string `json:"currency"`
	TotalAmount int64  `json:"total_amount"`
	// Payload is the invoice_payload from sendInvoice, the bot's own order key.
	Payload string `json:"invoice_payload"`
	// ShippingOptionID is reserved: telesrv settles Stars only and models no
	// shipping, so it is always empty.
	ShippingOptionID string `json:"shipping_option_id,omitempty"`
}

// BotPreCheckoutAnswer is what the bot sends back. Error is the message the payer
// sees when OK is false.
type BotPreCheckoutAnswer struct {
	OK    bool
	Error string
}

// BotCallbackQuery is the protocol-neutral payload of MTProto
// updateBotCallbackQuery, also handed to in-process service bots.
type BotCallbackQuery struct {
	ID            int64
	BotUserID     int64
	UserID        int64
	Peer          Peer
	MessageID     int
	ChatInstance  int64
	Data          []byte
	InlineMessage *BotInlineMessageID
	// ClientSession is available only to an in-process service bot. It must not
	// become part of a durable or wire-visible payload.
	ClientSession ClientSessionMetadata `json:"-"`
}

// BotInlineMessageID is the domain-only shape of inputBotInlineMessageID64,
// kept free of tg types so it can cross the store boundary.
type BotInlineMessageID struct {
	DCID       int
	OwnerID    int64
	ID         int
	AccessHash int64
}
