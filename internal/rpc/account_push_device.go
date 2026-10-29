package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// 设备登记的输入上限：APNs token 是 64 位 hex、iOS secret 是 256 字节，这里留足余量，
// 只防止异常大的输入写进存储。
const (
	maxPushDeviceTokenBytes  = 4096
	maxPushDeviceSecretBytes = 1024
	maxPushDeviceOtherUIDs   = 100
)

// onAccountRegisterDevice 保存（或覆盖）当前授权在该 token 类型下的推送登记。与 APNs 是否
// 配置无关：没配置时也照常保存，以后打开推送即可直接生效。登记挂在 perm auth key
// （authorizations 主键）上，iOS 走临时 key 时 AuthKeyIDFrom 已解析为绑定的 perm key。
func (r *Router) onAccountRegisterDevice(ctx context.Context, req *tg.AccountRegisterDeviceRequest) (bool, error) {
	if req == nil {
		return false, inputRequestInvalidErr()
	}
	if req.TokenType <= 0 {
		return false, tgerr.New(400, "TOKEN_TYPE_INVALID")
	}
	if req.Token == "" {
		return false, tgerr.New(400, "TOKEN_EMPTY")
	}
	if len(req.Token) > maxPushDeviceTokenBytes || len(req.Secret) > maxPushDeviceSecretBytes {
		return false, tgerr.New(400, "TOKEN_INVALID")
	}
	if len(req.OtherUIDs) > maxPushDeviceOtherUIDs {
		return false, limitInvalidErr()
	}
	userID, ok, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	authKeyID, hasAuthKey := AuthKeyIDFrom(ctx)
	if !ok || !hasAuthKey {
		return false, authKeyUnregisteredErr()
	}
	if r.deps.PushDevices == nil {
		return true, nil
	}
	info, _ := ClientInfoFrom(ctx)
	err = r.deps.PushDevices.RegisterPushDevice(ctx, domain.PushDevice{
		AuthKeyID:  authKeyID,
		UserID:     userID,
		TokenType:  req.TokenType,
		Token:      req.Token,
		AppSandbox: req.AppSandbox,
		Secret:     req.Secret,
		NoMuted:    req.NoMuted,
		OtherUIDs:  req.OtherUIDs,
		LangCode:   info.LangCode,
		// 只对新登记生效：同一授权重新登记时存储保留后台设置的推送开关。
		PushEnabled: r.cfg.PushNewDeviceEnabled,
	})
	if errors.Is(err, store.ErrPushDeviceAuthorizationNotFound) {
		return false, authKeyUnregisteredErr()
	}
	if err != nil {
		r.log.Warn("register push device failed", zap.Int64("user_id", userID), zap.Int("token_type", req.TokenType), zap.Error(err))
		return false, internalErr()
	}
	return true, nil
}

// onAccountUnregisterDevice 删除当前授权下该 token 类型、且 token 与 req.Token 相同的登记；
// 不存在（或已换成别的 token）时同样返回 true。token 轮换时 iOS 先登记新 token 再注销旧 token，
// 按 token 匹配才不会把刚登记的新 token 删掉。
func (r *Router) onAccountUnregisterDevice(ctx context.Context, req *tg.AccountUnregisterDeviceRequest) (bool, error) {
	if req == nil {
		return false, inputRequestInvalidErr()
	}
	if req.TokenType <= 0 {
		return false, tgerr.New(400, "TOKEN_TYPE_INVALID")
	}
	userID, ok, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	authKeyID, hasAuthKey := AuthKeyIDFrom(ctx)
	if !ok || !hasAuthKey {
		return false, authKeyUnregisteredErr()
	}
	if r.deps.PushDevices == nil {
		return true, nil
	}
	if _, err := r.deps.PushDevices.DeletePushDevice(ctx, authKeyID, req.TokenType, req.Token); err != nil {
		r.log.Warn("unregister push device failed", zap.Int64("user_id", userID), zap.Int("token_type", req.TokenType), zap.Error(err))
		return false, internalErr()
	}
	return true, nil
}
