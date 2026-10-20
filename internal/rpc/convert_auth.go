package rpc

import (
	"context"
	"telesrv/internal/domain"
)

// authzFromCtx 从连接上下文组装一条待绑定的设备授权（UserID 由业务层填充）。
func (r *Router) authzFromCtx(ctx context.Context) domain.Authorization {
	id, _ := AuthKeyIDFrom(ctx)
	a := domain.Authorization{AuthKeyID: id, Layer: LayerFrom(ctx)}
	if ci, ok := ClientInfoFrom(ctx); ok {
		a.DeviceModel = ci.DeviceModel
		a.Platform = string(ci.ClientType())
		a.SystemVersion = ci.SystemVersion
		a.AppVersion = ci.AppVersion
		a.APIID = ci.APIID
	}
	if ip, ok := ClientIPFrom(ctx); ok {
		a.IP = ip
	}
	return a
}

// currentUserID 返回当前连接已登录的 user_id。
//
// 优先使用 active session 缓存；若新连接尚未绑定但 auth_key 已授权，则只在这里
// 查询一次授权表并回填 session，避免各业务 service 每个 RPC 重复 authKey→userID。
func (r *Router) currentUserID(ctx context.Context) (int64, bool, error) {
	if userID, ok := UserIDFrom(ctx); ok {
		return userID, true, nil
	}
	if r.deps.Sessions != nil {
		if sessionID, ok := SessionIDFrom(ctx); ok {
			rawAuthKeyID := rawAuthKeyIDForOrigin(ctx)
			if userID, resolved := r.deps.Sessions.UserIDResolvedForAuthKey(rawAuthKeyID, sessionID); resolved {
				if userID == 0 {
					if authKeyID, ok := AuthKeyIDFrom(ctx); ok {
						if cachedUserID, ok := r.positiveCachedAuthUser(authKeyID); ok {
							r.deps.Sessions.BindUserForAuthKey(rawAuthKeyID, sessionID, cachedUserID)
							r.announceSessionOnline(ctx, cachedUserID)
							return cachedUserID, true, nil
						}
					}
				}
				return userID, userID != 0, nil
			}
		}
	}
	if r.deps.Auth == nil {
		return 0, false, nil
	}
	authKeyID, ok := AuthKeyIDFrom(ctx)
	if !ok {
		return 0, false, nil
	}
	userID, found, err := r.lookupAuthUser(ctx, authKeyID)
	if err != nil || !found {
		if err == nil && !found {
			r.bindSessionUser(ctx, 0)
		}
		return 0, found, err
	}
	r.bindSessionUser(ctx, userID)
	return userID, true, nil
}

// signedInAuthorization 返回当前连接的 auth_key 对应、属于 userID 且已完成登录（不是 2FA
// password_pending）的授权。ok=false 表示 context 里没有 auth_key、读不到授权，或授权不属于
// userID / 仍在等 2FA；err 只在读取失败时非空，已映射为 INTERNAL_SERVER_ERROR。
// 提现前的"刚登录"校验（payments）与忘记登录密码的重设窗口共用它。
func (r *Router) signedInAuthorization(ctx context.Context, userID int64) (domain.Authorization, bool, error) {
	authKeyID, ok := AuthKeyIDFrom(ctx)
	if !ok || authKeyID == ([8]byte{}) || r.deps.Auth == nil {
		return domain.Authorization{}, false, nil
	}
	authorization, found, err := r.deps.Auth.Authorization(ctx, authKeyID)
	if err != nil {
		return domain.Authorization{}, false, internalErr()
	}
	if !found || authorization.AuthKeyID != authKeyID || authorization.UserID != userID || authorization.PasswordPending {
		return domain.Authorization{}, false, nil
	}
	return authorization, true, nil
}
