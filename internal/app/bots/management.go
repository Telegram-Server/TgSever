package bots

import (
	"context"
	"crypto/subtle"
	"slices"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"time"
)

func (s *Service) managementStore() (store.BotManagementStore, error) {
	if s == nil || s.bots == nil {
		return nil, domain.ErrBotNotFound
	}
	st, ok := s.bots.(store.BotManagementStore)
	if !ok {
		return nil, domain.ErrBotManagerInvalid
	}
	return st, nil
}
func (s *Service) CreateManagedBot(ctx context.Context, ownerID, managerID int64, name, username string) (domain.User, string, error) {
	if s == nil || s.users == nil || s.bots == nil || managerID <= 0 {
		return domain.User{}, "", domain.ErrBotManagerInvalid
	}
	owner, found, err := s.users.ByID(ctx, ownerID)
	if err != nil {
		return domain.User{}, "", err
	}
	if !found || owner.Bot || owner.Deleted {
		return domain.User{}, "", domain.ErrBotManagerInvalid
	}
	if manager, found, err := s.bots.GetBot(ctx, managerID); err != nil {
		return domain.User{}, "", err
	} else if !found || !manager.CanManageBots {
		return domain.User{}, "", domain.ErrBotManagerInvalid
	}
	return s.createBot(ctx, ownerID, managerID, name, username)
}
func (s *Service) ManagedBot(ctx context.Context, managerID, botID int64) (domain.BotManagement, domain.BotProfile, error) {
	st, err := s.managementStore()
	if err != nil {
		return domain.BotManagement{}, domain.BotProfile{}, err
	}
	m, err := st.GetBotManagement(ctx, botID)
	if err != nil {
		return m, domain.BotProfile{}, err
	}
	if managerID <= 0 || m.ManagerBotID != managerID {
		return m, domain.BotProfile{}, domain.ErrBotManagerInvalid
	}
	p, found, err := s.bots.GetBot(ctx, botID)
	if err != nil {
		return m, p, err
	}
	if !found {
		return m, p, domain.ErrBotNotFound
	}
	return m, p, nil
}
func (s *Service) ManagedBotToken(ctx context.Context, managerID, botID int64, revoke bool) (string, error) {
	_, p, err := s.ManagedBot(ctx, managerID, botID)
	if err != nil {
		return "", err
	}
	if !revoke {
		if p.TokenSecret == "" {
			return "", domain.ErrBotNotFound
		}
		return domain.FormatBotToken(botID, p.TokenSecret), nil
	}
	// No successful rotation may leave a session authenticated with the old token.
	if s.hooks == nil {
		return "", domain.ErrBotSessionsNotRevoked
	}
	return s.RevokeBotToken(ctx, p.OwnerUserID, botID)
}
func (s *Service) BotAccessSettings(ctx context.Context, actorID, botID int64) (domain.BotManagement, error) {
	st, err := s.managementStore()
	if err != nil {
		return domain.BotManagement{}, err
	}
	p, found, err := s.bots.GetBot(ctx, botID)
	if err != nil {
		return domain.BotManagement{}, err
	}
	if !found {
		return domain.BotManagement{}, domain.ErrBotNotFound
	}
	m, err := st.GetBotManagement(ctx, botID)
	if err != nil {
		return m, err
	}
	if actorID != p.OwnerUserID && (actorID == 0 || actorID != m.ManagerBotID) {
		return domain.BotManagement{}, domain.ErrBotManagerInvalid
	}
	return m, nil
}
func (s *Service) SetBotAccessSettings(ctx context.Context, actorID, botID int64, restricted bool, added []int64) error {
	if _, err := s.BotAccessSettings(ctx, actorID, botID); err != nil {
		return err
	}
	if !restricted {
		added = nil
	}
	if len(added) > 10 {
		return domain.ErrBotAccessDenied
	}
	unique := make([]int64, 0, len(added))
	for _, id := range added {
		if id <= 0 {
			return domain.ErrBotAccessDenied
		}
		u, found, err := s.users.ByID(ctx, id)
		if err != nil {
			return err
		}
		if !found || u.Deleted {
			return domain.ErrBotAccessDenied
		}
		if !slices.Contains(unique, id) {
			unique = append(unique, id)
		}
	}
	st, err := s.managementStore()
	if err != nil {
		return err
	}
	return st.SetManagedBotAccess(ctx, actorID, botID, restricted, unique)
}
func (s *Service) BotAccessAllowed(ctx context.Context, botID, userID int64) (bool, error) {
	st, err := s.managementStore()
	if err != nil {
		return false, err
	}
	p, found, err := s.bots.GetBot(ctx, botID)
	if err != nil || !found {
		return false, err
	}
	m, err := st.GetBotManagement(ctx, botID)
	if err != nil {
		return false, err
	}
	return !m.IsAccessRestricted || p.OwnerUserID == userID || slices.Contains(m.AddedUserIDs, userID), nil
}
func (s *Service) AdmitBotAPIInstance(ctx context.Context, key domain.BotAPIInstanceKey, cloud bool, now time.Time) (domain.BotAPIInstance, error) {
	st, err := s.managementStore()
	if err != nil {
		return domain.BotAPIInstance{}, err
	}
	return st.BeginBotAPIInstance(ctx, key, cloud, now)
}
func (s *Service) StopBotAPIInstance(ctx context.Context, key domain.BotAPIInstanceKey, logout bool, now time.Time) error {
	st, err := s.managementStore()
	if err != nil {
		return err
	}
	return st.StopBotAPIInstance(ctx, key, logout, now)
}
func (s *Service) CanManageBots(ctx context.Context, botID int64) (bool, error) {
	if s == nil || s.bots == nil {
		return false, domain.ErrBotNotFound
	}
	p, found, err := s.bots.GetBot(ctx, botID)
	if err != nil || !found {
		return false, err
	}
	return p.CanManageBots, nil
}
func (s *Service) SetCanManageBots(ctx context.Context, ownerID, botID int64, enabled bool) error {
	st, err := s.managementStore()
	if err != nil {
		return err
	}
	if err = st.SetCanManageBots(ctx, ownerID, botID, enabled); err != nil {
		return err
	}
	s.invalidateBotReadCaches(ctx, botID)
	return nil
}
func (s *Service) ValidateBotToken(ctx context.Context, botID int64, secret string) (bool, error) {
	if s == nil || s.bots == nil || secret == "" {
		return false, nil
	}
	p, found, err := s.bots.GetBot(ctx, botID)
	if err != nil || !found {
		return false, err
	}
	return p.TokenSecret != "" && subtle.ConstantTimeCompare([]byte(p.TokenSecret), []byte(secret)) == 1, nil
}
