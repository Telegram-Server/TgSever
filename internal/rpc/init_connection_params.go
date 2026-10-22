package rpc

import (
	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"

	"telesrv/internal/domain"
)

// initConnectionDeviceIDKey 是 initConnection.params 顶层 JSON 对象里承载稳定设备标识的键。
const initConnectionDeviceIDKey = "device_id"

// deviceIDFromInitConnectionParams 从 initConnection.params 解析客户端上报的 device_id（#111）。
// 只认顶层 JSON 对象里的字符串键 device_id，且必须满足 domain.ValidDeviceID；缺失、
// 非字符串、不合法都返回空串（视为未上报）。重复键取第一个。
//
// params 来自两条路径：遗留 Router 分发拿到的是 tg.JSONValueClass；按 layer 的 wrapper
// 准入拿到的是 tlprofile 冻结后的规范对象，类型不保证是 tg 的具体类型，必要时按 wire
// 重新解码。
func deviceIDFromInitConnectionParams(params any) string {
	value, ok := initConnectionParamsJSON(params)
	if !ok {
		return ""
	}
	// 接口里装着 nil 指针（typed-nil）同样视为未上报，不能解引用。
	object, ok := value.(*tg.JSONObject)
	if !ok || object == nil {
		return ""
	}
	for _, field := range object.Value {
		if field.Key != initConnectionDeviceIDKey {
			continue
		}
		str, ok := field.Value.(*tg.JSONString)
		if !ok || str == nil || !domain.ValidDeviceID(str.Value) {
			return ""
		}
		return str.Value
	}
	return ""
}

func initConnectionParamsJSON(params any) (tg.JSONValueClass, bool) {
	switch typed := params.(type) {
	case nil:
		return nil, false
	case tg.JSONValueClass:
		return typed, typed != nil
	case bin.Encoder:
		var encoded bin.Buffer
		if err := typed.Encode(&encoded); err != nil {
			return nil, false
		}
		value, err := tg.DecodeJSONValue(&encoded)
		if err != nil || encoded.Len() != 0 {
			return nil, false
		}
		return value, true
	default:
		return nil, false
	}
}

// deviceIDFromLayerWrapper 读取 wrapper 准入路径上 initConnection 的可选 params。
// params 是可选字段，读不到或解析失败都按未上报处理，绝不因此拒绝 initConnection。
func deviceIDFromLayerWrapper(wrapper tlprofile.Wrapper) string {
	value, present, ok, err := wrapper.Value("params")
	if err != nil || !ok || !present {
		return ""
	}
	return deviceIDFromInitConnectionParams(value)
}
