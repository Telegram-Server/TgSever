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
	byType[device.TokenType] = device
	return nil
}

func (s *PushDeviceStore) ListPushDevicesByUser(_ context.Context, userID int64) ([]domain.PushDevice, error) {
	s.auths.mu.RLock()
	defer s.auths.mu.RUnlock()
	out := make([]domain.PushDevice, 0)
	for _, byType := range s.auths.pushDevices {
		for _, device := range byType {
			if device.UserID == userID {
				out = append(out, clonePushDevice(device))
			}
		}
	}
	return out, nil
}

func (s *PushDeviceStore) DeletePushDevice(_ context.Context, authKeyID [8]byte, tokenType int) (bool, error) {
	s.auths.mu.Lock()
	defer s.auths.mu.Unlock()
	byType := s.auths.pushDevices[authKeyID]
	if _, ok := byType[tokenType]; !ok {
		return false, nil
	}
	delete(byType, tokenType)
	if len(byType) == 0 {
		delete(s.auths.pushDevices, authKeyID)
	}
	return true, nil
}

func clonePushDevice(device domain.PushDevice) domain.PushDevice {
	device.Secret = append([]byte(nil), device.Secret...)
	device.OtherUIDs = append([]int64(nil), device.OtherUIDs...)
	return device
}
