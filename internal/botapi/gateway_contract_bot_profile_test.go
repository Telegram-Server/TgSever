package botapi

import "telesrv/internal/rpc"

// setMy*/getMy* 通过类型断言找 BotProfileGateway；签名漂移会让它们静默变成 501。
var _ BotProfileGateway = (*rpc.Router)(nil)
