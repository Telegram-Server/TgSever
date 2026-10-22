package botapi

import "net/http"

// logOut / close 是官方 Bot API 用来在云端与本地 Bot API server 之间迁移的方法。
// 本服务自己就是 Bot API server，没有可注销的云端会话、也没有要关闭的实例，
// 所以两者只在网关层固定返回 True，不触碰任何底层状态；官方"启动后 10 分钟内
// 调用 close 返回 429"的限制不照搬。从官方迁移过来的代码调用它们不会报错。
func init() {
	registerMethods(map[string]methodFunc{
		"logOut": lifecycleNoop,
		"close":  lifecycleNoop,
	})
}

func lifecycleNoop(_ *handler, w http.ResponseWriter, _ *http.Request, _ int64) {
	writeAPIOK(w, true)
}
