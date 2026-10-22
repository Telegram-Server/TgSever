package rpc

import (
	"context"
	"errors"
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

type botManagementService interface {
	ManagedBot(context.Context, int64, int64) (domain.BotManagement, domain.BotProfile, error)
	ManagedBotToken(context.Context, int64, int64, bool) (string, error)
	BotAccessSettings(context.Context, int64, int64) (domain.BotManagement, error)
	SetBotAccessSettings(context.Context, int64, int64, bool, []int64) error
}

func (r *Router) botManagementService() (botManagementService, error) {
	if r == nil || r.deps.Bots == nil {
		return nil, errors.New("BOT_INVALID")
	}
	s, ok := r.deps.Bots.(botManagementService)
	if !ok {
		return nil, errors.New("BOT_MANAGER_INVALID")
	}
	return s, nil
}
func (r *Router) BotAPIManagedBotToken(ctx context.Context, botID, userID int64, revoke bool) (string, error) {
	if !r.userIsBot(ctx, botID) {
		return "", errors.New("BOT_INVALID")
	}
	s, err := r.botManagementService()
	if err != nil {
		return "", err
	}
	token, err := s.ManagedBotToken(ctx, botID, userID, revoke)
	if revoke && token != "" {
		if _, profile, lookupErr := s.ManagedBot(ctx, botID, userID); lookupErr == nil {
			owner, ownerFound, ownerErr := r.deps.Users.ByID(ctx, botID, profile.OwnerUserID)
			child, childFound, childErr := r.deps.Users.ByID(ctx, botID, userID)
			if ownerErr == nil && childErr == nil && ownerFound && childFound {
				r.enqueueBotAPIObject(ctx, botID, domain.BotAPIUpdateKind("managed_bot"), map[string]any{"user": botAPIManagedPublicUser(owner), "bot": botAPIManagedPublicUser(child)})
			}
		}
	}
	return token, err
}
func (r *Router) BotAPIGetManagedBotAccessSettings(ctx context.Context, botID, userID int64) (domain.BotManagement, []domain.User, error) {
	if !r.userIsBot(ctx, botID) {
		return domain.BotManagement{}, nil, errors.New("BOT_INVALID")
	}
	s, err := r.botManagementService()
	if err != nil {
		return domain.BotManagement{}, nil, err
	}
	m, _, err := s.ManagedBot(ctx, botID, userID)
	if err != nil {
		return domain.BotManagement{}, nil, err
	}
	return m, r.domainUsersForIDs(ctx, botID, m.AddedUserIDs), nil
}
func (r *Router) BotAPISetManagedBotAccessSettings(ctx context.Context, botID, userID int64, restricted bool, added []int64) error {
	if !r.userIsBot(ctx, botID) {
		return errors.New("BOT_INVALID")
	}
	s, err := r.botManagementService()
	if err != nil {
		return err
	}
	if _, _, err = s.ManagedBot(ctx, botID, userID); err != nil {
		return err
	}
	return s.SetBotAccessSettings(ctx, botID, userID, restricted, added)
}
func botAPIManagedPublicUser(u domain.User) map[string]any {
	out := map[string]any{"id": u.ID, "is_bot": u.Bot, "first_name": u.FirstName}
	if u.LastName != "" {
		out["last_name"] = u.LastName
	}
	if u.Username != "" {
		out["username"] = u.Username
	}
	return out
}
func (r *Router) botAccessSettingsNative(ctx context.Context, actorID, botID int64) (*tg.BotsAccessSettings, error) {
	s, err := r.botManagementService()
	if err != nil {
		return nil, botInvalidErr()
	}
	m, err := s.BotAccessSettings(ctx, actorID, botID)
	if err != nil {
		return nil, botInvalidErr()
	}
	out := &tg.BotsAccessSettings{Restricted: m.IsAccessRestricted}
	if len(m.AddedUserIDs) > 0 {
		out.SetAddUsers(r.tgUsers(r.domainUsersForIDs(ctx, actorID, m.AddedUserIDs)))
	}
	return out, nil
}
