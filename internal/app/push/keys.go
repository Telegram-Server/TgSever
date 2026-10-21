package push

// Key 是推送的内部分类键，同时是 iOS 语言包 "PUSH_"+Key 的后缀。
// 私聊 MESSAGE_*，超级群 CHAT_MESSAGE_*，频道 CHANNEL_MESSAGE_*（#97/#98 扩充）。
type Key string

const (
	KeyMessageText   Key = "MESSAGE_TEXT"
	KeyMessageNoText Key = "MESSAGE_NOTEXT"
	// KeyEncryptedMessage 渲染外层通用文案（"You have a new message"），不含任何消息内容。
	KeyEncryptedMessage Key = "ENCRYPTED_MESSAGE"
)

// messageAlert 选出新消息的文案键与语言包参数。
//
// 本期只区分文字（MESSAGE_TEXT：发送者、正文）与其他（MESSAGE_NOTEXT：发送者）。
// 扩展点：#97 在这里按媒体类型细分键与参数；#98 加群组/频道的 CHAT_/CHANNEL_ 前缀与会话标题参数；
// #96 的“关闭预览”通过 showPreview=false 走 *_NOTEXT，payload 里不出现正文。
func messageAlert(msg NewMessage, senderName string, showPreview bool) (Key, []string) {
	if showPreview && msg.Media == nil && msg.Text != "" {
		return KeyMessageText, []string{senderName, msg.Text}
	}
	return KeyMessageNoText, []string{senderName}
}
