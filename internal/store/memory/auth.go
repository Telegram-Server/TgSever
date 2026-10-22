package memory

import (
	"bytes"
	"context"
	"encoding/binary"
	"sort"
	"sync"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type authKeyState struct {
	mu                   sync.RWMutex
	keys                 map[[8]byte]store.AuthKeyData
	bindings             map[[8]byte]domain.TempAuthKeyBinding
	sessionLayers        map[authKeySessionLayerKey]store.AuthKeySessionLayer
	authorizationMirrors map[*AuthorizationStore]struct{}
	nextLayerObservation int64
}

// AuthKeyStore 是 store.AuthKeyStore 的内存实现。
type AuthKeyStore struct {
	state *authKeyState
}

// NewAuthKeyStore 创建内存 AuthKeyStore。
func NewAuthKeyStore() *AuthKeyStore {
	return &AuthKeyStore{state: &authKeyState{
		keys:                 make(map[[8]byte]store.AuthKeyData),
		bindings:             make(map[[8]byte]domain.TempAuthKeyBinding),
		sessionLayers:        make(map[authKeySessionLayerKey]store.AuthKeySessionLayer),
		authorizationMirrors: make(map[*AuthorizationStore]struct{}),
	}}
}

func (s *AuthKeyStore) Save(_ context.Context, k store.AuthKeyData) error {
	if !store.ValidNewAuthKeyProtocolExpiry(k.ExpiresAt) {
		return store.ErrInvalidAuthKeyProtocolExpiry
	}
	s.state.mu.Lock()
	if current, ok := s.state.keys[k.ID]; ok {
		if current.Value != k.Value || current.ExpiresAt != k.ExpiresAt {
			s.state.mu.Unlock()
			return store.ErrAuthKeyProtocolMetadataConflict
		}
		// Save is the idempotent protocol-key upsert. Match PostgreSQL's
		// ON CONFLICT behavior: a repeated handshake may refresh server_salt,
		// but must never erase Layer/client metadata recorded after the first
		// insert merely because the protocol write carries zero-value metadata.
		if k.CreatedAt == 0 {
			k.CreatedAt = current.CreatedAt
		}
		k.Layer = current.Layer
		k.LayerObservationID = current.LayerObservationID
		k.DeviceModel = current.DeviceModel
		k.Platform = current.Platform
		k.SystemVersion = current.SystemVersion
		k.APIID = current.APIID
		k.AppVersion = current.AppVersion
		k.DeviceID = current.DeviceID
	}
	s.state.keys[k.ID] = k
	s.state.mirrorAuthorizationLayersLocked([][8]byte{k.ID}, k.Layer)
	// Tests may restore a store snapshot carrying an already-issued durable
	// observation. Keep the in-memory sequence above that watermark so the next
	// AdvanceSessionLayer has the same monotonic ordering as PostgreSQL's
	// sequence after a restart/fixture restore.
	if k.LayerObservationID > s.state.nextLayerObservation {
		s.state.nextLayerObservation = k.LayerObservationID
	}
	s.state.mu.Unlock()
	return nil
}

func (s *AuthKeyStore) Get(_ context.Context, id [8]byte) (store.AuthKeyData, bool, error) {
	s.state.mu.RLock()
	k, ok := s.state.keys[id]
	s.state.mu.RUnlock()
	return k, ok, nil
}

func (s *AuthKeyStore) Revalidate(ctx context.Context, id [8]byte) (store.AuthKeyData, bool, error) {
	return s.Get(ctx, id)
}

func (s *AuthKeyStore) LoadBindingKeys(_ context.Context, tempID, permID [8]byte) (store.AuthKeyBindingKeys, error) {
	s.state.mu.RLock()
	temp, tempFound := s.state.keys[tempID]
	perm, permFound := s.state.keys[permID]
	s.state.mu.RUnlock()
	return store.AuthKeyBindingKeys{
		Temporary:      temp,
		TemporaryFound: tempFound,
		Permanent:      perm,
		PermanentFound: permFound,
	}, nil
}

func (s *AuthKeyStore) UpdateClientInfo(_ context.Context, id [8]byte, info store.AuthKeyClientInfo) (bool, error) {
	s.state.mu.Lock()
	k, ok := s.state.keys[id]
	if !ok {
		s.state.mu.Unlock()
		return false, store.ErrAuthKeyNotFound
	}
	if info.Layer > 0 && k.LayerObservationID > 0 && info.Layer != k.Layer {
		s.state.mu.Unlock()
		return false, store.ErrAuthKeySessionLayerConflict
	}
	deviceIDStored := mergeAuthKeyClientInfo(&k, info)
	s.state.keys[id] = k
	s.state.mirrorAuthorizationLayersLocked([][8]byte{id}, k.Layer)
	s.state.mu.Unlock()
	return deviceIDStored, nil
}

// mergeAuthKeyClientInfo 合并客户端信息，报告 device_id 是否本次新写入。
func mergeAuthKeyClientInfo(k *store.AuthKeyData, info store.AuthKeyClientInfo) (deviceIDStored bool) {
	if info.Layer > 0 {
		k.Layer = info.Layer
	}
	if info.DeviceModel != "" {
		k.DeviceModel = info.DeviceModel
	}
	if info.Platform != "" {
		k.Platform = info.Platform
	}
	if info.SystemVersion != "" {
		k.SystemVersion = info.SystemVersion
	}
	if info.APIID != 0 {
		k.APIID = info.APIID
	}
	if info.AppVersion != "" {
		k.AppVersion = info.AppVersion
	}
	// device_id 一次写入：只在从未上报时写，之后不再改变（见 store.AuthKeyClientInfo.DeviceID）。
	if info.DeviceID != "" && k.DeviceID == "" {
		k.DeviceID = info.DeviceID
		deviceIDStored = true
	}
	return deviceIDStored
}

// mirrorAuthorizationLayersLocked updates the materialized authorization view
// at the same write boundary as auth_keys. authKeyState.mu must be held; the
// only cross-object lock order is auth-key state -> authorization mirror.
func (s *authKeyState) mirrorAuthorizationLayersLocked(ids [][8]byte, layer int) {
	for mirror := range s.authorizationMirrors {
		mirror.mu.Lock()
		for _, id := range ids {
			if a, found := mirror.m[id]; found {
				a.Layer = layer
				mirror.m[id] = a
			}
		}
		mirror.mu.Unlock()
	}
}

func (s *authKeyState) deleteAuthorizationMirrorsLocked(ids [][8]byte) {
	s.deleteAuthorizationMirrorsWithHeldLocked(ids, nil)
}

func (s *authKeyState) deleteAuthorizationMirrorsWithHeldLocked(ids [][8]byte, held *AuthorizationStore) {
	for mirror := range s.authorizationMirrors {
		if mirror != held {
			mirror.mu.Lock()
		}
		for _, id := range ids {
			mirror.removeLocked(id)
		}
		if mirror != held {
			mirror.mu.Unlock()
		}
	}
}

func (s *AuthKeyStore) Delete(_ context.Context, id [8]byte) error {
	s.state.mu.Lock()
	deleted := s.state.deleteProtocolAuthKeyLocked(id)
	s.state.deleteAuthorizationMirrorsLocked(deleted)
	s.state.mu.Unlock()
	return nil
}

func (s *authKeyState) deleteProtocolAuthKeyLocked(id [8]byte) [][8]byte {
	deleting, exists := s.keys[id]
	if !exists {
		return nil
	}
	deleted := make([][8]byte, 0, 2)
	if deleting.ExpiresAt > 0 {
		delete(s.bindings, id)
	} else {
		permID := int64(binary.LittleEndian.Uint64(id[:]))
		for tempID, binding := range s.bindings {
			if binding.PermAuthKeyID != permID {
				continue
			}
			delete(s.bindings, tempID)
			delete(s.keys, tempID)
			s.deleteSessionLayersLocked(tempID)
			deleted = append(deleted, tempID)
		}
	}
	delete(s.keys, id)
	s.deleteSessionLayersLocked(id)
	return append(deleted, id)
}

// TempAuthKeyBindingStore 是 store.TempAuthKeyBindingStore 的内存实现。
type TempAuthKeyBindingStore struct {
	state *authKeyState
}

// NewTempAuthKeyBindingStore 创建内存 TempAuthKeyBindingStore。
func NewTempAuthKeyBindingStore(authKeys *AuthKeyStore) *TempAuthKeyBindingStore {
	if authKeys == nil {
		panic("memory.NewTempAuthKeyBindingStore requires a non-nil AuthKeyStore")
	}
	return &TempAuthKeyBindingStore{state: authKeys.state}
}

func (s *TempAuthKeyBindingStore) Save(ctx context.Context, b domain.TempAuthKeyBinding) error {
	_, err := s.SaveWithState(ctx, b)
	return err
}

func (s *TempAuthKeyBindingStore) SaveWithState(_ context.Context, b domain.TempAuthKeyBinding) (domain.TempAuthKeyBindingResult, error) {
	b.EncryptedMessage = append([]byte(nil), b.EncryptedMessage...)
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if current, ok := s.state.bindings[b.TempAuthKeyID]; ok && current.PermAuthKeyID != b.PermAuthKeyID {
		return domain.TempAuthKeyBindingResult{}, store.ErrTempAuthKeyAlreadyBound
	}
	temp, tempFound := s.state.keys[b.TempAuthKeyID]
	var permID [8]byte
	binary.LittleEndian.PutUint64(permID[:], uint64(b.PermAuthKeyID))
	perm, permFound := s.state.keys[permID]
	if !tempFound || !permFound || temp.ExpiresAt <= 0 || perm.ExpiresAt != 0 || b.ExpiresAt != temp.ExpiresAt {
		return domain.TempAuthKeyBindingResult{}, store.ErrAuthKeyBindingInvalid
	}
	// Binding and Layer-default normalization are one state transition. Exact
	// session evidence remains keyed by the raw temp key; only the inherited
	// default follows the globally ordered observation.
	layer, observationID, err := store.MergeAuthKeyLayerObservations(
		temp.Layer, temp.LayerObservationID,
		perm.Layer, perm.LayerObservationID,
	)
	if err != nil {
		return domain.TempAuthKeyBindingResult{}, err
	}
	temp.Layer, temp.LayerObservationID = layer, observationID
	perm.Layer, perm.LayerObservationID = layer, observationID
	s.state.keys[b.TempAuthKeyID] = temp
	s.state.keys[permID] = perm
	s.state.bindings[b.TempAuthKeyID] = b
	s.state.mirrorAuthorizationLayersLocked([][8]byte{b.TempAuthKeyID, permID}, layer)
	return domain.TempAuthKeyBindingResult{Layer: layer, LayerObservationID: observationID}, nil
}

func (s *TempAuthKeyBindingStore) GetByTemp(_ context.Context, tempAuthKeyID [8]byte) (domain.TempAuthKeyBinding, bool, error) {
	s.state.mu.RLock()
	b, ok := s.state.bindings[tempAuthKeyID]
	s.state.mu.RUnlock()
	if !ok {
		return domain.TempAuthKeyBinding{}, false, nil
	}
	b.EncryptedMessage = append([]byte(nil), b.EncryptedMessage...)
	return b, true, nil
}

func (s *TempAuthKeyBindingStore) DeleteExpired(_ context.Context, expiredBefore int64, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	deleted := 0
	for id, key := range s.state.keys {
		if deleted >= limit {
			break
		}
		if key.ExpiresAt <= 0 || int64(key.ExpiresAt) >= expiredBefore {
			continue
		}
		delete(s.state.bindings, id)
		delete(s.state.keys, id)
		s.state.deleteSessionLayersLocked(id)
		s.state.deleteAuthorizationMirrorsLocked([][8]byte{id})
		deleted++
	}
	return deleted, nil
}

// AuthorizationStore 是 store.AuthorizationStore 的内存实现。
type AuthorizationStore struct {
	linkMu   sync.RWMutex
	authKeys *authKeyState
	// users 由 LinkAuthorizationUsers 接上（受 linkMu 保护），见 deviceLimitApplies。
	users store.UserStore
	mu    sync.RWMutex
	m     map[[8]byte]domain.Authorization
	// deviceLimits 是账号的设备上限（受 mu 保护），与授权在同一把锁下计数和写入。
	deviceLimits map[int64]int
	// deviceBindings 是账号的设备绑定名单（#112，受 mu 保护）：userID → device_id → 绑定。
	// 删除授权（removeLocked）不碰它——退出登录、撤销授权都不解除绑定；只有账号注销
	// （DeleteDeviceBindings）删除整份名单。
	deviceBindings map[int64]map[string]domain.DeviceBinding
	// pushDevices 是挂在授权上的设备推送登记（见 PushDeviceStore），与 m 同受 mu 保护，
	// 删除授权的每条路径都经 removeLocked 一并清掉。
	pushDevices map[[8]byte]map[int]domain.PushDevice
}

// removeLocked 删除授权及其设备推送登记。调用方持有 s.mu。
func (s *AuthorizationStore) removeLocked(id [8]byte) {
	delete(s.m, id)
	delete(s.pushDevices, id)
}

// NewAuthorizationStore 创建内存 AuthorizationStore。
func NewAuthorizationStore() *AuthorizationStore {
	return &AuthorizationStore{
		m:           make(map[[8]byte]domain.Authorization),
		pushDevices: make(map[[8]byte]map[int]domain.PushDevice),
	}
}

// LinkAuthKeyAuthority connects the test/dev in-memory projection to the same
// auth-key state. PostgreSQL performs the equivalent projection updates inside
// its write transactions. Existing standalone AuthorizationStore construction
// remains valid for tests that intentionally omit protocol keys.
func (s *AuthorizationStore) LinkAuthKeyAuthority(keys store.AuthKeyStore) {
	authKeys, ok := keys.(*AuthKeyStore)
	if !ok || authKeys == nil || authKeys.state == nil {
		return
	}
	s.linkMu.Lock()
	defer s.linkMu.Unlock()
	if s.authKeys == authKeys.state {
		return
	}
	if s.authKeys != nil {
		// A projection has one authoritative primary. Constructors do not
		// return errors, so preserve the first explicit composition.
		return
	}
	authKeys.state.mu.Lock()
	s.mu.Lock()
	for id, a := range s.m {
		if key, found := authKeys.state.keys[id]; found {
			a.Layer = key.Layer
			s.m[id] = a
		}
	}
	if authKeys.state.authorizationMirrors == nil {
		authKeys.state.authorizationMirrors = make(map[*AuthorizationStore]struct{})
	}
	authKeys.state.authorizationMirrors[s] = struct{}{}
	s.authKeys = authKeys.state
	s.mu.Unlock()
	authKeys.state.mu.Unlock()
}

func (s *AuthorizationStore) Bind(ctx context.Context, a domain.Authorization) error {
	limited, err := s.deviceLimitApplies(ctx, a.UserID)
	if err != nil {
		return err
	}
	now := time.Now()
	if a.Hash == 0 {
		a.Hash = int64(binary.LittleEndian.Uint64(a.AuthKeyID[:]))
	}
	// Bind is an explicit login boundary. Metadata-only refreshes use
	// UpdateClientInfo and must not reset the session age.
	a.CreatedAt = now
	a.ActiveAt = now
	s.linkMu.RLock()
	if s.authKeys != nil {
		s.authKeys.mu.RLock()
		key, found := s.authKeys.keys[a.AuthKeyID]
		if !found {
			s.authKeys.mu.RUnlock()
			s.linkMu.RUnlock()
			return store.ErrAuthKeyNotFound
		}
		if key.ExpiresAt != 0 {
			s.authKeys.mu.RUnlock()
			s.linkMu.RUnlock()
			return store.ErrAuthKeyNotPermanent
		}
		a.Layer = key.Layer
		s.mu.Lock()
		err := s.bindLocked(a, limited, key.DeviceID)
		s.mu.Unlock()
		s.authKeys.mu.RUnlock()
		s.linkMu.RUnlock()
		return err
	}
	s.linkMu.RUnlock()
	// 没有接上 auth key 数据时读不到 device_id，按"未上报"处理。
	s.mu.Lock()
	err = s.bindLocked(a, limited, "")
	s.mu.Unlock()
	return err
}

// bindLocked 在同一把锁下完成"判定 + 写入"（#112 绑定规则）：账号有设备上限且这次是
// 完成登录（不是 password_pending）时，按 deviceID 绑定或拒绝；拒绝时不写入任何东西。
func (s *AuthorizationStore) bindLocked(a domain.Authorization, limited bool, deviceID string) error {
	if limited && !a.PasswordPending {
		if err := s.enforceDeviceBindingLocked(a, deviceID, true, time.Now()); err != nil {
			return err
		}
	}
	s.m[a.AuthKeyID] = a
	return nil
}

// enforceDeviceBindingLocked 对 a 这次"完成登录"执行绑定规则 3–6（规则 1 的 bot 由调用方
// 用 deviceLimitApplies 排除，规则 2 的 password_pending 由调用方跳过）：账号没有上限 →
// 放行；deviceID 不合法 → ErrDeviceIDRequired；已绑定 → 放行（write 时刷新最近使用时间）；
// 绑定数 < 上限 → 放行（write 时写入绑定）；否则 ErrDeviceLimitReached。调用方持有 mu，
// write=true 时必须是写锁。
func (s *AuthorizationStore) enforceDeviceBindingLocked(a domain.Authorization, deviceID string, write bool, now time.Time) error {
	slotFree, err := s.checkDeviceBindingLocked(a.UserID, deviceID, write, now)
	if err == nil && slotFree && write {
		s.insertDeviceBindingLocked(a, deviceID, now)
	}
	return err
}

// checkDeviceBindingLocked 是绑定规则 3–6 的判定部分，登录（enforceDeviceBindingLocked）与
// 延迟补绑（BackfillDeviceBinding）共用：没有上限、已绑定 → (false, nil)；deviceID 不合法 →
// ErrDeviceIDRequired；尚未绑定且绑定数 < 上限 → (true, nil)，是否写入由调用方决定；否则
// ErrDeviceLimitReached。touchIfBound 时已绑定的设备刷新最近使用时间（需持有 mu 写锁）。
func (s *AuthorizationStore) checkDeviceBindingLocked(userID int64, deviceID string, touchIfBound bool, now time.Time) (slotFree bool, err error) {
	limit, ok := s.deviceLimits[userID]
	if !ok {
		return false, nil
	}
	if !domain.ValidDeviceID(deviceID) {
		return false, domain.ErrDeviceIDRequired
	}
	bound := s.deviceBindings[userID]
	if b, ok := bound[deviceID]; ok {
		if touchIfBound {
			b.LastUsedAt = now
			bound[deviceID] = b
		}
		return false, nil
	}
	if len(bound) >= limit {
		return false, domain.ErrDeviceLimitReached
	}
	return true, nil
}

// insertDeviceBindingLocked 把 deviceID 写进 a.UserID 的绑定名单，设备信息取自授权 a。
// 调用方持有 mu 写锁，并已判定名额。
func (s *AuthorizationStore) insertDeviceBindingLocked(a domain.Authorization, deviceID string, now time.Time) {
	bound := s.deviceBindings[a.UserID]
	if bound == nil {
		if s.deviceBindings == nil {
			s.deviceBindings = make(map[int64]map[string]domain.DeviceBinding)
		}
		bound = make(map[string]domain.DeviceBinding)
		s.deviceBindings[a.UserID] = bound
	}
	bound[deviceID] = domain.DeviceBinding{
		UserID:      a.UserID,
		DeviceID:    deviceID,
		DeviceModel: a.DeviceModel,
		Platform:    a.Platform,
		AppVersion:  a.AppVersion,
		BoundAt:     now,
		LastUsedAt:  now,
	}
}

// backfillDeviceBindingsLocked 是设上限时的补绑（#115）：按登录时间先后遍历账号的已完成
// 授权，把 auth key 上有合法 device_id、尚未绑定的设备绑定，直到绑定数达到 limit；返回
// 补绑数。write=false 只计数（试运行）。调用方持有 mu（write 时写锁），且在 authKeys 不为
// nil 时持有 authKeys.mu 读锁（锁序 linkMu → authKeys.mu → mu）。
func (s *AuthorizationStore) backfillDeviceBindingsLocked(userID int64, limit int, write bool, now time.Time) int {
	if s.authKeys == nil {
		return 0
	}
	completed := make([]domain.Authorization, 0)
	for _, a := range s.m {
		if a.UserID == userID && !a.PasswordPending {
			completed = append(completed, a)
		}
	}
	sort.Slice(completed, func(i, j int) bool {
		if !completed[i].CreatedAt.Equal(completed[j].CreatedAt) {
			return completed[i].CreatedAt.Before(completed[j].CreatedAt)
		}
		return bytes.Compare(completed[i].AuthKeyID[:], completed[j].AuthKeyID[:]) < 0
	})
	bound := s.deviceBindings[userID]
	used := len(bound)
	planned := make(map[string]struct{})
	backfilled := 0
	for _, a := range completed {
		if used >= limit {
			break
		}
		deviceID := s.authKeys.keys[a.AuthKeyID].DeviceID
		if !domain.ValidDeviceID(deviceID) {
			continue
		}
		if _, ok := bound[deviceID]; ok {
			continue
		}
		if _, ok := planned[deviceID]; ok {
			continue
		}
		planned[deviceID] = struct{}{}
		used++
		backfilled++
		if write {
			// 同一次补绑里按登录先后错开绑定时间，名单的顺序与登录先后一致。
			s.insertDeviceBindingLocked(a, deviceID, now.Add(time.Duration(backfilled)))
			bound = s.deviceBindings[userID]
		}
	}
	return backfilled
}

// authKeyDeviceID 读 auth key 上持久化的 device_id；没有接上 auth key 数据或 key 不存在时
// 返回空串（按"未上报"处理）。不持有 mu 时调用。
func (s *AuthorizationStore) authKeyDeviceID(id [8]byte) string {
	s.linkMu.RLock()
	defer s.linkMu.RUnlock()
	if s.authKeys == nil {
		return ""
	}
	s.authKeys.mu.RLock()
	defer s.authKeys.mu.RUnlock()
	return s.authKeys.keys[id].DeviceID
}

// ListDeviceBindings 按绑定时间先后（同一时刻按 device_id）返回账号的设备绑定名单。
func (s *AuthorizationStore) ListDeviceBindings(_ context.Context, userID int64) ([]domain.DeviceBinding, error) {
	s.mu.RLock()
	out := make([]domain.DeviceBinding, 0, len(s.deviceBindings[userID]))
	for _, b := range s.deviceBindings[userID] {
		out = append(out, b)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if !out[i].BoundAt.Equal(out[j].BoundAt) {
			return out[i].BoundAt.Before(out[j].BoundAt)
		}
		return out[i].DeviceID < out[j].DeviceID
	})
	return out, nil
}

// DeleteDeviceBindings 删除账号的整份设备绑定名单，只供账号注销使用（用户是软删除，
// 没有外键级联）。退出登录、撤销授权、调整上限都不调用它。
func (s *AuthorizationStore) DeleteDeviceBindings(_ context.Context, userID int64) error {
	s.mu.Lock()
	delete(s.deviceBindings, userID)
	s.mu.Unlock()
	return nil
}

// UnbindDevice 是后台解绑（#114），见 store.DeviceLimitStore。删绑定与撤销授权在同一组锁下
// 完成（锁序 linkMu → authKeys.mu → mu），与登录的绑定判定互斥。
func (s *AuthorizationStore) UnbindDevice(_ context.Context, userID int64, deviceID string) ([]domain.Authorization, error) {
	unlock := s.lockAuthKeysForRead()
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	bound := s.deviceBindings[userID]
	if _, ok := bound[deviceID]; !ok {
		return nil, domain.ErrDeviceBindingNotFound
	}
	delete(bound, deviceID)
	revoked := s.authorizationsOnDeviceLocked(userID, deviceID)
	for _, a := range revoked {
		// 只删业务授权，协议 key 保留：与 RevokeByHash 一样让客户端重连后收到 401。
		s.removeLocked(a.AuthKeyID)
	}
	return revoked, nil
}

// PreviewUnbindDevice 只读地报告解绑会撤销的授权数（试运行）。
func (s *AuthorizationStore) PreviewUnbindDevice(_ context.Context, userID int64, deviceID string) (int, error) {
	unlock := s.lockAuthKeysForRead()
	defer unlock()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.deviceBindings[userID][deviceID]; !ok {
		return 0, domain.ErrDeviceBindingNotFound
	}
	return len(s.authorizationsOnDeviceLocked(userID, deviceID)), nil
}

// authorizationsOnDeviceLocked 返回 userID 在 device_id 为 deviceID 的 auth key 上的全部授权，
// 按登录先后。调用方持有 mu，且在 authKeys 不为 nil 时持有 authKeys.mu。
func (s *AuthorizationStore) authorizationsOnDeviceLocked(userID int64, deviceID string) []domain.Authorization {
	out := make([]domain.Authorization, 0)
	if s.authKeys == nil {
		return out
	}
	for id, a := range s.m {
		if a.UserID == userID && s.authKeys.keys[id].DeviceID == deviceID {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return bytes.Compare(out[i].AuthKeyID[:], out[j].AuthKeyID[:]) < 0
	})
	return out
}

// LinkAuthorizationUsers 接上同一份用户数据：Bind 据此跳过 bot，SetDeviceLimit 据此拒绝
// 不存在的账号。只接受第一次接入，与 LinkAuthKeyAuthority 相同。
func (s *AuthorizationStore) LinkAuthorizationUsers(users store.UserStore) {
	if users == nil {
		return
	}
	s.linkMu.Lock()
	if s.users == nil {
		s.users = users
	}
	s.linkMu.Unlock()
}

func (s *AuthorizationStore) linkedUsers() store.UserStore {
	s.linkMu.RLock()
	defer s.linkMu.RUnlock()
	return s.users
}

// deviceLimitApplies 报告设备上限是否作用于 userID：bot 不受限。没有接上用户数据时一律
// 按普通账号处理。用户事实在拿 mu 之前读出（是否 bot 不会变），不嵌套两个 store 的锁。
func (s *AuthorizationStore) deviceLimitApplies(ctx context.Context, userID int64) (bool, error) {
	users := s.linkedUsers()
	if users == nil {
		return true, nil
	}
	u, found, err := users.ByID(ctx, userID)
	if err != nil {
		return false, err
	}
	return !found || !u.Bot, nil
}

func (s *AuthorizationStore) GetDeviceLimit(_ context.Context, userID int64) (int, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	limit, ok := s.deviceLimits[userID]
	return limit, ok, nil
}

// SetDeviceLimit 写上限并在同一把锁下补绑（#115），返回补绑数。bot 只写上限、不补绑。
func (s *AuthorizationStore) SetDeviceLimit(ctx context.Context, userID int64, limit int) (int, error) {
	if !domain.ValidDeviceLimit(limit) {
		return 0, domain.ErrDeviceLimitInvalid
	}
	limited := true
	if users := s.linkedUsers(); users != nil {
		u, found, err := users.ByID(ctx, userID)
		if err != nil {
			return 0, err
		}
		if !found {
			return 0, domain.ErrUserNotFound
		}
		limited = !u.Bot
	}
	unlock := s.lockAuthKeysForRead()
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deviceLimits == nil {
		s.deviceLimits = make(map[int64]int)
	}
	s.deviceLimits[userID] = limit
	if !limited {
		return 0, nil
	}
	return s.backfillDeviceBindingsLocked(userID, limit, true, time.Now()), nil
}

// PreviewDeviceLimitBackfill 只读地计算把上限设为 limit 时会补绑的设备数。
func (s *AuthorizationStore) PreviewDeviceLimitBackfill(ctx context.Context, userID int64, limit int) (int, error) {
	if !domain.ValidDeviceLimit(limit) {
		return 0, domain.ErrDeviceLimitInvalid
	}
	limited, err := s.deviceLimitApplies(ctx, userID)
	if err != nil || !limited {
		return 0, err
	}
	unlock := s.lockAuthKeysForRead()
	defer unlock()
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.backfillDeviceBindingsLocked(userID, limit, false, time.Time{}), nil
}

// BackfillDeviceBinding 是延迟补绑（#115）：见 store.DeviceLimitStore。
func (s *AuthorizationStore) BackfillDeviceBinding(ctx context.Context, authKeyID [8]byte) (bool, error) {
	s.mu.RLock()
	a, ok := s.m[authKeyID]
	_, hasLimit := s.deviceLimits[a.UserID]
	s.mu.RUnlock()
	if !ok || a.PasswordPending || !hasLimit {
		return false, nil
	}
	limited, err := s.deviceLimitApplies(ctx, a.UserID)
	if err != nil || !limited {
		return false, err
	}
	deviceID := s.authKeyDeviceID(authKeyID)
	if !domain.ValidDeviceID(deviceID) {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.m[authKeyID]
	if !ok || current.UserID != a.UserID || current.PasswordPending {
		return false, nil
	}
	// 与登录同一套规则；名额已满等拒绝在补绑里只是"不绑定"，已绑定的设备不刷新最近使用时间。
	if slotFree, err := s.checkDeviceBindingLocked(current.UserID, deviceID, false, time.Time{}); err != nil || !slotFree {
		return false, nil
	}
	s.insertDeviceBindingLocked(current, deviceID, time.Now())
	return true, nil
}

// lockAuthKeysForRead 按锁序（linkMu → authKeys.mu）拿读锁，供随后在 mu 下读 key 上的
// device_id；返回的函数释放这两把锁。
func (s *AuthorizationStore) lockAuthKeysForRead() func() {
	s.linkMu.RLock()
	keys := s.authKeys
	if keys == nil {
		return s.linkMu.RUnlock
	}
	keys.mu.RLock()
	return func() {
		keys.mu.RUnlock()
		s.linkMu.RUnlock()
	}
}

func (s *AuthorizationStore) CheckDeviceLimit(ctx context.Context, userID int64, authKeyID [8]byte) error {
	limited, err := s.deviceLimitApplies(ctx, userID)
	if err != nil || !limited {
		return err
	}
	deviceID := s.authKeyDeviceID(authKeyID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.enforceDeviceBindingLocked(domain.Authorization{UserID: userID, AuthKeyID: authKeyID}, deviceID, false, time.Time{})
}

func (s *AuthorizationStore) ClearDeviceLimit(_ context.Context, userID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.deviceLimits[userID]
	delete(s.deviceLimits, userID)
	return ok, nil
}

func (s *AuthorizationStore) ByAuthKey(_ context.Context, id [8]byte) (domain.Authorization, bool, error) {
	s.mu.RLock()
	a, ok := s.m[id]
	s.mu.RUnlock()
	return a, ok, nil
}

func (s *AuthorizationStore) UpdateClientInfo(_ context.Context, id [8]byte, info domain.AuthKeyClientInfo) error {
	s.linkMu.RLock()
	if s.authKeys != nil {
		s.authKeys.mu.RLock()
		key, found := s.authKeys.keys[id]
		if !found {
			s.authKeys.mu.RUnlock()
			s.linkMu.RUnlock()
			return store.ErrAuthKeyNotFound
		}
		s.mu.Lock()
		if a, ok := s.m[id]; ok {
			mergeAuthorizationClientInfo(&a, info)
			a.Layer = key.Layer
			s.m[id] = a
		}
		s.mu.Unlock()
		s.authKeys.mu.RUnlock()
		s.linkMu.RUnlock()
		return nil
	}
	s.linkMu.RUnlock()
	s.mu.Lock()
	if a, ok := s.m[id]; ok {
		mergeAuthorizationClientInfo(&a, info)
		s.m[id] = a
	}
	s.mu.Unlock()
	return nil
}

func mergeAuthorizationClientInfo(a *domain.Authorization, info domain.AuthKeyClientInfo) {
	if info.Layer > 0 {
		a.Layer = info.Layer
	}
	if info.DeviceModel != "" {
		a.DeviceModel = info.DeviceModel
	}
	if info.Platform != "" {
		a.Platform = info.Platform
	}
	if info.SystemVersion != "" {
		a.SystemVersion = info.SystemVersion
	}
	if info.APIID != 0 {
		a.APIID = info.APIID
	}
	if info.AppVersion != "" {
		a.AppVersion = info.AppVersion
	}
	if info.IP != "" {
		a.IP = info.IP
	}
	a.ActiveAt = time.Now()
}

// MarkPasswordPassed 转正是一次"完成登录"，在同一把锁下执行绑定规则（#106、#112）：
// 绑定或放行；被拒绝（domain.ErrDeviceIDRequired / domain.ErrDeviceLimitReached）时删除
// 这条待完成的授权并返回该错误。
func (s *AuthorizationStore) MarkPasswordPassed(ctx context.Context, id [8]byte, expectedUserID int64) error {
	limited, err := s.deviceLimitApplies(ctx, expectedUserID)
	if err != nil {
		return err
	}
	deviceID := s.authKeyDeviceID(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.m[id]
	if !ok || expectedUserID == 0 || a.UserID != expectedUserID || !a.PasswordPending {
		return store.ErrAuthorizationStateChanged
	}
	now := time.Now()
	if limited {
		if err := s.enforceDeviceBindingLocked(a, deviceID, true, now); err != nil {
			s.removeLocked(id)
			return err
		}
	}
	a.PasswordPending = false
	a.CreatedAt = now
	a.ActiveAt = now
	s.m[id] = a
	return nil
}

func (s *AuthorizationStore) ListByUser(_ context.Context, userID int64) ([]domain.Authorization, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Authorization, 0)
	for _, a := range s.m {
		if a.UserID == userID {
			out = append(out, a)
		}
	}
	return out, nil
}

func (s *AuthorizationStore) Delete(_ context.Context, id [8]byte) error {
	s.mu.Lock()
	s.removeLocked(id)
	s.mu.Unlock()
	return nil
}

func (s *AuthorizationStore) DeleteByHash(_ context.Context, userID, hash int64) (domain.Authorization, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, a := range s.m {
		if a.UserID == userID && a.Hash == hash {
			s.removeLocked(id)
			return a, true, nil
		}
	}
	return domain.Authorization{}, false, nil
}

// RevokeByHash removes only the business authorization. The protocol auth key
// and any temp binding stay usable for MTProto decryption so a kicked client can
// reconnect and receive AUTH_KEY_UNREGISTERED from the RPC gate.
func (s *AuthorizationStore) RevokeByHash(ctx context.Context, userID, hash int64) (domain.Authorization, bool, error) {
	s.linkMu.RLock()
	defer s.linkMu.RUnlock()
	if s.authKeys == nil {
		return s.DeleteByHash(ctx, userID, hash)
	}
	s.authKeys.mu.Lock()
	s.mu.Lock()
	for id, a := range s.m {
		if a.UserID == userID && a.Hash == hash {
			s.removeLocked(id)
			s.mu.Unlock()
			s.authKeys.mu.Unlock()
			return a, true, nil
		}
	}
	s.mu.Unlock()
	s.authKeys.mu.Unlock()
	return domain.Authorization{}, false, nil
}

func (s *AuthorizationStore) DeleteByUserExcept(_ context.Context, userID int64, keepAuthKeyID [8]byte) ([]domain.Authorization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Authorization, 0)
	for id, a := range s.m {
		if a.UserID != userID || id == keepAuthKeyID {
			continue
		}
		s.removeLocked(id)
		out = append(out, a)
	}
	return out, nil
}

func (s *AuthorizationStore) RevokeByUserExcept(ctx context.Context, userID int64, keepAuthKeyID [8]byte) ([]domain.Authorization, error) {
	s.linkMu.RLock()
	defer s.linkMu.RUnlock()
	if s.authKeys == nil {
		return s.DeleteByUserExcept(ctx, userID, keepAuthKeyID)
	}
	s.authKeys.mu.Lock()
	s.mu.Lock()
	out := make([]domain.Authorization, 0)
	for id, a := range s.m {
		if a.UserID == userID && id != keepAuthKeyID {
			out = append(out, a)
			s.removeLocked(id)
		}
	}
	s.mu.Unlock()
	s.authKeys.mu.Unlock()
	return out, nil
}

// CodeStore 是 store.CodeStore 的内存实现（带 TTL）。
type CodeStore struct {
	mu     sync.Mutex
	m      map[string]codeEntry
	scopes map[store.PhoneCodeScope]string
}

// NewCodeStore 创建内存 CodeStore。
func NewCodeStore() *CodeStore {
	return &CodeStore{
		m:      make(map[string]codeEntry),
		scopes: make(map[store.PhoneCodeScope]string),
	}
}

func (s *CodeStore) Set(_ context.Context, hash string, code store.PhoneCode, ttl time.Duration) error {
	revision, err := store.NewPhoneCodeRevisionToken()
	if err != nil {
		return err
	}
	code.Revision = revision
	s.mu.Lock()
	scope := code.Scope()
	if scope.Valid() {
		if oldHash, ok := s.scopes[scope]; ok && oldHash != hash {
			delete(s.m, oldHash)
		}
		s.scopes[scope] = hash
	}
	s.m[hash] = codeEntry{code: code, expires: time.Now().Add(ttl)}
	s.mu.Unlock()
	return nil
}

func (s *CodeStore) Get(_ context.Context, hash string) (store.PhoneCode, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[hash]
	if !ok || time.Now().After(e.expires) {
		if ok {
			s.deleteCodeLocked(hash, e.code)
		}
		return store.PhoneCode{}, false, nil
	}
	return e.code, true, nil
}

func (s *CodeStore) Update(_ context.Context, hash string, code store.PhoneCode) error {
	revision, err := store.NewPhoneCodeRevisionToken()
	if err != nil {
		return err
	}
	code.Revision = revision
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[hash]
	if !ok || time.Now().After(e.expires) {
		if ok {
			s.deleteCodeLocked(hash, e.code)
		}
		return nil
	}
	e.code = code
	s.m[hash] = e
	return nil
}

func (s *CodeStore) Del(_ context.Context, hash string) error {
	s.mu.Lock()
	if e, ok := s.m[hash]; ok {
		s.deleteCodeLocked(hash, e.code)
	} else {
		delete(s.m, hash)
	}
	s.mu.Unlock()
	return nil
}

func (s *CodeStore) ConsumeScoped(_ context.Context, hash string, scope store.PhoneCodeScope) (store.PhoneCode, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !scope.Valid() || s.scopes[scope] != hash {
		return store.PhoneCode{}, false, nil
	}
	e, ok := s.m[hash]
	if !ok || time.Now().After(e.expires) {
		if ok {
			s.deleteCodeLocked(hash, e.code)
		} else {
			delete(s.scopes, scope)
		}
		return store.PhoneCode{}, false, nil
	}
	if e.code.Version != store.PhoneCodeVersionCurrent || e.code.Scope() != scope {
		delete(s.m, hash)
		delete(s.scopes, scope)
		actualScope := e.code.Scope()
		if actualScope.Valid() && s.scopes[actualScope] == hash {
			delete(s.scopes, actualScope)
		}
		return store.PhoneCode{}, false, nil
	}
	s.deleteCodeLocked(hash, e.code)
	return e.code, true, nil
}

func (s *CodeStore) deleteCodeLocked(hash string, code store.PhoneCode) {
	delete(s.m, hash)
	scope := code.Scope()
	if scope.Valid() && s.scopes[scope] == hash {
		delete(s.scopes, scope)
	}
}
