package domain

// MaxDeviceIDLength 是 device_id 的最大字节数（#111）。
const MaxDeviceIDLength = 128

// ValidDeviceID 判断客户端上报的 device_id 是否合法：1–MaxDeviceIDLength 字节的可打印
// ASCII（0x20–0x7E）。服务端把它当作不透明字符串；不合法的值一律按"未上报"处理，
// 不进入持久化，也不参与设备绑定。
func ValidDeviceID(deviceID string) bool {
	if len(deviceID) == 0 || len(deviceID) > MaxDeviceIDLength {
		return false
	}
	for i := 0; i < len(deviceID); i++ {
		if c := deviceID[i]; c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}
