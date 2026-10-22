package store

import (
	"context"
	"telesrv/internal/domain"
)

type BotGamesPassportStore interface {
	CreateBotGame(context.Context, domain.BotGame) error
	GetBotGame(context.Context, int64, string) (domain.BotGame, bool, error)
	GetBotGameByID(context.Context, int64) (domain.BotGame, bool, error)
	SetBotGameScore(context.Context, domain.GameScoreScope, int64, int, bool) error
	BotGameScores(context.Context, domain.GameScoreScope) ([]domain.GameScore, error)
	BusinessGameMessageRefs(context.Context, int64, int64, int) ([]domain.BusinessGameMessageRef, error)
	SetPassportPublicKey(context.Context, int64, string) error
	PassportPublicKey(context.Context, int64) (string, error)
	GetPassportData(context.Context, int64) (domain.PassportUserData, error)
	SavePassportElement(context.Context, int64, domain.PassportElement) error
	DeletePassportElements(context.Context, int64, []string) error
	SetPassportErrors(context.Context, int64, int64, []domain.PassportError) error
}
