package main

import (
	"telesrv/internal/botapi"
	"telesrv/internal/rpc"
)

// Bot API 的可选网关面都是在运行时对 rpc.Router 做类型断言取到的：Router 与 botapi 之间的
// 签名一旦漂移，编译不会失败，对应方法只会悄悄退化成 501 METHOD_NOT_FOUND。这里是唯一同时
// 依赖两个包的地方，所以在生产二进制里钉住 Router 必须满足的全部契约；放在 botapi 测试里
// 会让那个测试二进制链接整个 rpc 包。
var (
	_ botapi.StarGiftGatewayService  = (*rpc.Router)(nil)
	_ botapi.PremiumGatewayService   = (*rpc.Router)(nil)
	_ botapi.EphemeralGatewayService = (*rpc.Router)(nil)
	_ botapi.ChatRefGateway          = (*rpc.Router)(nil)
	_ botapi.BotProfileGateway       = (*rpc.Router)(nil)
	_ botapi.ChatAdminGateway        = (*rpc.Router)(nil)
	_ botapi.ChatQueryGateway        = (*rpc.Router)(nil)
	_ botapi.ChatInviteLinkGateway   = (*rpc.Router)(nil)
	_ botapi.ForwardGateway          = (*rpc.Router)(nil)
	_ botapi.SendExtraGateway        = (*rpc.Router)(nil)
	_ botapi.EditExtraGateway        = (*rpc.Router)(nil)
	_ botapi.MenuButtonGateway       = (*rpc.Router)(nil)
)
