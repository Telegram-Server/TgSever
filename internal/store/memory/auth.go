package memory

import (
	"context"
	"encoding/binary"
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

func (s *AuthKeyStore) UpdateClientInfo(_ context.Context, id [8]byte, info store.AuthKeyClientInfo) error {
	s.state.mu.Lock()
	k, ok := s.state.keys[id]
	if !ok {
		s.state.mu.Unlock()
		return store.ErrAuthKeyNotFound
	}
	if info.Layer > 0 && k.LayerObservationID > 0 && info.Layer != k.Layer {
		s.state.mu.Unlock()
		return store.ErrAuthKeySessionLayerConflict
	}
	mergeAuthKeyClientInfo(&k, info)
	s.state.keys[id] = k
	s.state.mirrorAuthorizationLayersLocked([][8]byte{id}, k.Layer)
	s.state.mu.Unlock()
	return nil
}

func mergeAuthKeyClientInfo(k *store.AuthKeyData, info store.AuthKeyClientInfo) {
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
		err := s.bindLocked(a, limited)
		s.mu.Unlock()
		s.authKeys.mu.RUnlock()
		s.linkMu.RUnlock()
		return err
	}
	s.linkMu.RUnlock()
	s.mu.Lock()
	err = s.bindLocked(a, limited)
	s.mu.Unlock()
	return err
}

// bindLocked 在同一把锁下完成"计数 + 写入"：账号有设备上限时，已完成登录的其它授权
// （不算当前 auth key 自身、不算 password_pending）达到上限就拒绝，不写入。
func (s *AuthorizationStore) bindLocked(a domain.Authorization, limited bool) error {
	if limited {
		if limit, ok := s.deviceLimits[a.UserID]; ok && s.completedOthersLocked(a.UserID, a.AuthKeyID) >= limit {
			return domain.ErrDeviceLimitReached
		}
	}
	s.m[a.AuthKeyID] = a
	return nil
}

func (s *AuthorizationStore) completedOthersLocked(userID int64, self [8]byte) int {
	n := 0
	for id, existing := range s.m {
		if existing.UserID == userID && id != self && !existing.PasswordPending {
			n++
		}
	}
	return n
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

func (s *AuthorizationStore) SetDeviceLimit(ctx context.Context, userID int64, limit int) error {
	if !domain.ValidDeviceLimit(limit) {
		return domain.ErrDeviceLimitInvalid
	}
	if users := s.linkedUsers(); users != nil {
		if _, found, err := users.ByID(ctx, userID); err != nil {
			return err
		} else if !found {
			return domain.ErrUserNotFound
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deviceLimits == nil {
		s.deviceLimits = make(map[int64]int)
	}
	s.deviceLimits[userID] = limit
	return nil
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

func (s *AuthorizationStore) MarkPasswordPassed(_ context.Context, id [8]byte, expectedUserID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.m[id]
	if !ok || expectedUserID == 0 || a.UserID != expectedUserID || !a.PasswordPending {
		return store.ErrAuthorizationStateChanged
	}
	now := time.Now()
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
