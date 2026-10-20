package main

import (
	"telesrv/internal/domain"
	"telesrv/internal/identity"
)

// codeLoginStartupWarning 返回启动时关于验证码登录实际状态的提示，没有需要提示的情况时
// 返回空串。规则与 auth.IdentityCodeLoginAllowed 相同：
//   - 密码登录开启而验证码登录实际关闭（覆盖值或环境变量关闭，或 identity.json 读不出按关闭）：
//     提示已关闭；
//   - 密码登录关闭时验证码登录一定开启：identity.json 里若有"关闭"覆盖值，提示它被忽略。
func codeLoginStartupWarning(store *identity.Store, envDefault, passwordLoginEnabled bool) string {
	var info identity.Info
	if store != nil {
		var err error
		info, err = store.Get()
		if err != nil {
			if passwordLoginEnabled {
				return "验证码登录已关闭：identity.json 读取失败，按关闭处理（fail closed），只能用密码、扫码或 passkey 登录：" + err.Error()
			}
			return ""
		}
	}
	if !passwordLoginEnabled {
		if info.CodeLoginEnabled != nil && !*info.CodeLoginEnabled {
			return "管理面板保存的验证码登录关闭覆盖值被忽略：密码登录总开关 TELESRV_PASSWORD_LOGIN_ENABLED 未开启，验证码登录保持开启（防锁死）"
		}
		return ""
	}
	if !domain.ResolveCodeLoginEnabled(info.CodeLoginEnabled, envDefault, passwordLoginEnabled) {
		return "验证码登录已关闭：不签发、不投递任何登录验证码，只能用密码、扫码或 passkey 登录"
	}
	return ""
}
