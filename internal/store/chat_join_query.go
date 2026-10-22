package store

import (
	"context"
	"telesrv/internal/domain"
)

type ChatJoinQueryStore interface {
	SetChannelGuardBot(context.Context, int64, int64, int64, bool) (domain.Channel, error)
	GetChannelGuardBot(context.Context, int64) (int64, error)
	CreateChatJoinQuery(context.Context, domain.ChatJoinQuery) (domain.ChatJoinQuery, error)
	ExpireChatJoinQueries(context.Context, int, int) ([]domain.ChatJoinQueryOutcome, error)
	GetChatJoinQuery(context.Context, int64) (domain.ChatJoinQuery, error)
	ResolveChatJoinQuery(context.Context, int64, int64, string, string, int) (domain.ChatJoinQuery, domain.CreateChannelResult, error)
}
