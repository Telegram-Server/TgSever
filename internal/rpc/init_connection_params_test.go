package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"
)

// typed-nil 的 params（接口里装着 nil 指针）按未上报处理，不能解引用 panic。
func TestDeviceIDFromInitConnectionParamsTreatsTypedNilAsUnreported(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params any
	}{
		{name: "nil object", params: (*tg.JSONObject)(nil)},
		{name: "nil object as JSONValueClass", params: tg.JSONValueClass((*tg.JSONObject)(nil))},
		{name: "nil string", params: (*tg.JSONString)(nil)},
		{name: "nil device_id value", params: &tg.JSONObject{Value: []tg.JSONObjectValue{
			{Key: "device_id", Value: (*tg.JSONString)(nil)},
		}}},
		{name: "nil device_id interface", params: &tg.JSONObject{Value: []tg.JSONObjectValue{
			{Key: "device_id"},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := deviceIDFromInitConnectionParams(tc.params); got != "" {
				t.Fatalf("device id = %q, want unreported", got)
			}
		})
	}
}
