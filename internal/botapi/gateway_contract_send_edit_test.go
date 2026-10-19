package botapi

import "telesrv/internal/rpc"

// 发送与编辑扩展方法经运行时类型断言取到网关，签名漂移不会让编译失败，只会把
// 方法悄悄变成 501；在这里钉住生产网关必须满足的契约。
var (
	_ SendExtraGateway = (*rpc.Router)(nil)
	_ EditExtraGateway = (*rpc.Router)(nil)
)
