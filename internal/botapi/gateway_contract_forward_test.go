package botapi

import "telesrv/internal/rpc"

// 转发/复制组的可选网关契约：签名漂移会让四个方法悄悄退化成 501。
var _ ForwardGateway = (*rpc.Router)(nil)
