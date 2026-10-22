package memory

import (
	"context"
	"math"
	"slices"
	"telesrv/internal/domain"
	"time"
)

func (s *BotStore) GetBotManagement(_ context.Context, botID int64) (domain.BotManagement, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.byID[botID]; !ok {
		return domain.BotManagement{}, domain.ErrBotNotFound
	}
	m := s.management[botID]
	m.BotUserID = botID
	m.AddedUserIDs = slices.Clone(m.AddedUserIDs)
	return m, nil
}
func (s *BotStore) SetBotManager(_ context.Context, ownerID, botID, managerID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[botID]
	if !ok || p.OwnerUserID != ownerID {
		return domain.ErrBotManagerInvalid
	}
	if managerID == botID {
		return domain.ErrBotManagerInvalid
	}
	if manager, ok := s.byID[managerID]; !ok || !manager.CanManageBots {
		return domain.ErrBotManagerInvalid
	}
	if s.management == nil {
		s.management = make(map[int64]domain.BotManagement)
	}
	m := s.management[botID]
	m.BotUserID, m.ManagerBotID = botID, managerID
	s.management[botID] = m
	return nil
}
func (s *BotStore) SetManagedBotAccess(_ context.Context, actorID, botID int64, restricted bool, added []int64) error {
	if len(added) > 10 {
		return domain.ErrBotAccessDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[botID]
	if !ok {
		return domain.ErrBotManagerInvalid
	}
	m := s.management[botID]
	if p.OwnerUserID != actorID && (m.ManagerBotID == 0 || m.ManagerBotID != actorID) {
		return domain.ErrBotManagerInvalid
	}
	if s.management == nil {
		s.management = make(map[int64]domain.BotManagement)
	}
	m.BotUserID, m.IsAccessRestricted, m.AddedUserIDs = botID, restricted, slices.Clone(added)
	s.management[botID] = m
	return nil
}
func (s *BotStore) BeginBotAPIInstance(_ context.Context, key domain.BotAPIInstanceKey, cloud bool, now time.Time) (domain.BotAPIInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[key.BotUserID]; !ok {
		return domain.BotAPIInstance{}, domain.ErrBotNotFound
	}
	if cloud {
		for k, v := range s.instances {
			if k.BotUserID == key.BotUserID && !v.LoggedOutAt.IsZero() && now.Before(v.LoggedOutAt.Add(10*time.Minute)) {
				return domain.BotAPIInstance{}, &domain.BotAPIInstanceWaitError{Seconds: int(math.Ceil(v.LoggedOutAt.Add(10 * time.Minute).Sub(now).Seconds()))}
			}
		}
	}
	if v, ok := s.instances[key]; ok {
		if !v.ClosedAt.IsZero() {
			return v, domain.ErrBotInstanceClosed
		}
		return v, nil
	}
	if s.instances == nil {
		s.instances = make(map[domain.BotAPIInstanceKey]domain.BotAPIInstance)
	}
	v := domain.BotAPIInstance{BotUserID: key.BotUserID, InstanceID: key.InstanceID, Cloud: cloud, StartedAt: now}
	s.instances[key] = v
	return v, nil
}
func (s *BotStore) StopBotAPIInstance(_ context.Context, key domain.BotAPIInstanceKey, logout bool, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.instances[key]
	if !ok {
		return domain.ErrBotInstanceClosed
	}
	if !v.ClosedAt.IsZero() {
		return domain.ErrBotInstanceClosed
	}
	if !logout && now.Before(v.StartedAt.Add(10*time.Minute)) {
		return &domain.BotAPIInstanceWaitError{Seconds: int(math.Ceil(v.StartedAt.Add(10 * time.Minute).Sub(now).Seconds()))}
	}
	v.ClosedAt = now
	if logout && v.Cloud {
		v.LoggedOutAt = now
	}
	s.instances[key] = v
	return nil
}
func (s *BotStore) SetCanManageBots(_ context.Context, ownerID, botID int64, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[botID]
	if !ok || p.OwnerUserID != ownerID {
		return domain.ErrBotNotFound
	}
	p.CanManageBots = enabled
	s.byID[botID] = p
	return nil
}
