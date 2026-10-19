package botapi

import (
	"fmt"
	"net/http"
	"strings"

	"telesrv/internal/domain"
)

// 注册表让各组方法住在自己的文件里：分发 switch、错误码白名单和消息投影原本
// 都是一处集中的列表，每加一个方法就得改同一个文件。新方法在本组文件的 init
// 里登记，server.go 和 projection.go 只在末尾查一次表。

// methodFunc 是一个注册到网关的 Bot API 方法。
type methodFunc func(h *handler, w http.ResponseWriter, r *http.Request, botID int64)

var registeredMethods = map[string]methodFunc{}

// registerMethods 登记一组方法；名字不区分大小写。重名直接 panic——两个文件
// 抢同一个方法名是编码错误，宁可启动即失败也不要静默覆盖。
func registerMethods(methods map[string]methodFunc) {
	for name, fn := range methods {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			panic("botapi: empty method name")
		}
		if _, dup := registeredMethods[key]; dup {
			panic(fmt.Sprintf("botapi: method %q registered twice", name))
		}
		registeredMethods[key] = fn
	}
}

var registeredErrorMarkers []string

// registerErrorMarkers 把 TL 错误码加进 apiErrorDescription 的白名单；未登记的
// 错误一律折成 BAD_REQUEST。
func registerErrorMarkers(markers ...string) {
	registeredErrorMarkers = append(registeredErrorMarkers, markers...)
}

var messageProjectionExtensions []func(msg domain.Message, out map[string]any)

// registerMessageProjection 给 apiMessage 追加一个字段投影，在内置字段之后运行。
func registerMessageProjection(fn func(msg domain.Message, out map[string]any)) {
	messageProjectionExtensions = append(messageProjectionExtensions, fn)
}
