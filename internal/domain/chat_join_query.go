package domain

import "errors"

var ErrChatJoinQueryInvalid = errors.New("QUERY_ID_INVALID")
var ErrChatJoinQueryResultInvalid = errors.New("QUERY_RESULT_INVALID")

// ChatJoinQuery belongs to one pending request and one assigned guard bot.
// Pending queries require a first answer in ten seconds; a Web App extends
// the decision window without consuming the final, single-use decision.
type ChatJoinQuery struct {
	ID          int64
	ChannelID   int64
	BotUserID   int64
	UserID      int64
	RequestDate int
	ExpiresAt   int
	State       string
	WebAppURL   string
}

func (q ChatJoinQuery) CanResolve(botID int64, now int, result string) bool {
	return q.ID > 0 && q.BotUserID == botID && q.ExpiresAt > now && (q.State == "pending" || q.State == "webview") && (result != "webview" || q.State == "pending")
}

type ChatJoinQueryOutcome struct {
	Query  ChatJoinQuery
	Result CreateChannelResult
}
