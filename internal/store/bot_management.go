package store

import (
	"context"
	"telesrv/internal/domain"
	"time"
)

// BotManagementStore updates manager rights under the same lock as authorization.
type BotManagementStore interface {
	GetBotManagement(context.Context, int64) (domain.BotManagement, error)
	SetBotManager(ctx context.Context, ownerID, botID, managerID int64) error
	SetManagedBotAccess(ctx context.Context, actorID, botID int64, restricted bool, added []int64) error
	SetCanManageBots(ctx context.Context, ownerID, botID int64, enabled bool) error
	// BeginBotAPIInstance rejects a closed instance and the cloud logout cooldown.
	BeginBotAPIInstance(ctx context.Context, key domain.BotAPIInstanceKey, cloud bool, now time.Time) (domain.BotAPIInstance, error)
	StopBotAPIInstance(ctx context.Context, key domain.BotAPIInstanceKey, logout bool, now time.Time) error
}
