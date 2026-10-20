package domain

import "strings"

// reservedUsernamePrefix 是定制 Telegram Desktop 客户端更新源频道使用的用户名前缀。
// 客户端会通过 MTProto 解析 tdhbcfeed（及带数字后缀的变体）频道寻找更新，
// 因此任何用户或频道都不能占用以它开头的用户名，防止冒充更新源。
const reservedUsernamePrefix = "tdhbc"

// ReservedUsername 报告 username 是否为保留用户名：经 NormalizeUsername
// 规范化并转为小写后以 tdhbc 开头即保留。只用于设置/检查用户名的校验路径，
// 解析路径不调用它。
func ReservedUsername(username string) bool {
	return strings.HasPrefix(strings.ToLower(NormalizeUsername(username)), reservedUsernamePrefix)
}
