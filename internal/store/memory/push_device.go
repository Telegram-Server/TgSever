package memory

import (
	"context"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// PushDeviceStore 是 store.PushDeviceStore 的内存实现。登记直接存放在 AuthorizationStore
// 里、与授权同一把锁，删除授权的所有路径都会顺带清掉登记（对应 postgres 的外键级联）。
type PushDeviceStore struct {
	auths *AuthorizationStore
}

var _ store.PushDeviceStore = (*PushDeviceStore)(nil)

// NewPushDeviceStore 创建挂在 auths 上的内存设备登记存储。
func NewPushDeviceStore(auths *AuthorizationStore) *PushDeviceStore {
	if auths == nil {
		panic("memory.NewPushDeviceStore requires a non-nil AuthorizationStore")
	}
	return &PushDeviceStore{auths: auths}
}

func (s *PushDeviceStore) RegisterPushDevice(_ context.Context, device domain.PushDevice) error {
	device = clonePushDevice(device)
	device.CreatedAt = time.Now()
	device.AuthorizationUserID = 0
	s.auths.mu.Lock()
	defer s.auths.mu.Unlock()
	if _, ok := s.auths.m[device.AuthKeyID]; !ok {
		return store.ErrPushDeviceAuthorizationNotFound
	}
	byType := s.auths.pushDevices[device.AuthKeyID]
	if byType == nil {
		byType = make(map[int]domain.PushDevice)
		s.auths.pushDevices[device.AuthKeyID] = byType
	}
	// 覆盖已有登记时保留已存的推送开关（对应 postgres 的 ON CONFLICT 不更新 push_enabled）。
	if current, ok := byType[device.TokenType]; ok {
		device.PushEnabled = current.PushEnabled
	}
	byType[device.TokenType] = device
	return nil
}

func (s *PushDeviceStore) ListPushDevicesByUser(_ context.Context, userID int64) ([]domain.PushDevice, error) {
	s.auths.mu.RLock()
	defer s.auths.mu.RUnlock()
	out := make([]domain.PushDevice, 0)
	for authKeyID, byType := range s.auths.pushDevices {
		for _, device := range byType {
			if device.UserID == userID {
				out = append(out, s.withAuthorizationUserLocked(authKeyID, device))
			}
		}
	}
	return out, nil
}

func (s *PushDeviceStore) ListPushDevicesByUsers(_ context.Context, userIDs []int64) ([]domain.PushDevice, error) {
	want := make(map[int64]struct{}, len(userIDs))
	for _, id := range userIDs {
		want[id] = struct{}{}
	}
	s.auths.mu.RLock()
	defer s.auths.mu.RUnlock()
	out := make([]domain.PushDevice, 0)
	for authKeyID, byType := range s.auths.pushDevices {
		for _, device := range byType {
			if _, ok := want[device.UserID]; ok {
				out = append(out, s.withAuthorizationUserLocked(authKeyID, device))
			}
		}
	}
	return out, nil
}

func (s *PushDeviceStore) DeletePushDevice(_ context.Context, authKeyID [8]byte, tokenType int, token string) (bool, error) {
	s.auths.mu.Lock()
	defer s.auths.mu.Unlock()
	byType := s.auths.pushDevices[authKeyID]
	if current, ok := byType[tokenType]; !ok || current.Token != token {
		return false, nil
	}
	delete(byType, tokenType)
	if len(byType) == 0 {
		delete(s.auths.pushDevices, authKeyID)
	}
	return true, nil
}

func (s *PushDeviceStore) DeleteStalePushDevice(_ context.Context, device domain.PushDevice) (bool, error) {
	s.auths.mu.Lock()
	defer s.auths.mu.Unlock()
	byType := s.auths.pushDevices[device.AuthKeyID]
	current, ok := byType[device.TokenType]
	if !ok || current.Token != device.Token || current.UserID != device.UserID {
		return false, nil
	}
	delete(byType, device.TokenType)
	if len(byType) == 0 {
		delete(s.auths.pushDevices, device.AuthKeyID)
	}
	return true, nil
}

func (s *PushDeviceStore) SetPushDevicesEnabled(_ context.Context, userID int64, authKeyID [8]byte, enabled bool) (int, error) {
	s.auths.mu.Lock()
	defer s.auths.mu.Unlock()
	updated := 0
	for tokenType, device := range s.auths.pushDevices[authKeyID] {
		if device.UserID != userID {
			continue
		}
		device.PushEnabled = enabled
		s.auths.pushDevices[authKeyID][tokenType] = device
		updated++
	}
	return updated, nil
}

// withAuthorizationUserLocked 返回登记的副本，带上授权当前的 user（调用方持有 auths.mu）。
func (s *PushDeviceStore) withAuthorizationUserLocked(authKeyID [8]byte, device domain.PushDevice) domain.PushDevice {
	device = clonePushDevice(device)
	device.AuthorizationUserID = s.auths.m[authKeyID].UserID
	return device
}

func clonePushDevice(device domain.PushDevice) domain.PushDevice {
	device.Secret = append([]byte(nil), device.Secret...)
	device.OtherUIDs = append([]int64(nil), device.OtherUIDs...)
	return device
}
