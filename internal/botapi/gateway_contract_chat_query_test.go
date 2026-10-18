package botapi

import "telesrv/internal/rpc"

// 查询类方法通过运行时类型断言取网关；签名漂移只会让它们静默变成 501，所以在这里钉住。
var _ ChatQueryGateway = (*rpc.Router)(nil)
