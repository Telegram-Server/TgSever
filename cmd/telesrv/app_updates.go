package main

import (
	appupdatesapp "telesrv/internal/app/appupdates"
	"telesrv/internal/rpc"
)

// appUpdateDocumentProvider 把可能为 nil 的 *Registry 转成 rpc 依赖：未启用更新服务时返回
// nil 接口，而不是 typed nil（rpc 的依赖校验会拒绝 typed nil）。
func appUpdateDocumentProvider(registry *appupdatesapp.Registry) rpc.AppUpdateDocumentProvider {
	if registry == nil {
		return nil
	}
	return registry
}
