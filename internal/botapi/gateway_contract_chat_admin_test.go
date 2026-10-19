package botapi

import "telesrv/internal/rpc"

// 群管理方法经运行时类型断言取网关，签名漂移不会编译失败，只会悄悄变成 501。
var _ ChatAdminGateway = (*rpc.Router)(nil)
