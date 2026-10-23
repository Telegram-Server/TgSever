package domain

// BotStoppedUpdate 是 updateBotStopped（#148）：用户在私聊里拉黑（Stopped=true）或解除
// 拉黑（Stopped=false）了 bot。TDLib 把它转成私聊的 my_chat_member，bot 据此停止或恢复
// 给该用户推送。
type BotStoppedUpdate struct {
	UserID  int64
	Stopped bool
}

// BotStoppedChange 生成 userID 拉黑或解除拉黑 botUserID 对应的 qts 记录。只在拉黑状态
// 确实变化时调用；对方是不是 bot 由调用方（store，在同一事务内）判定。
func BotStoppedChange(userID, botUserID int64, stopped bool, date int) BotUpdate {
	return BotUpdate{
		BotUserID:  botUserID,
		Date:       date,
		BotStopped: &BotStoppedUpdate{UserID: userID, Stopped: stopped},
	}
}
