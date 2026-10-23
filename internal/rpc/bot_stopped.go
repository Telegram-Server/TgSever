package rpc

import (
	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// tgBotStoppedUpdate 把 updateBotStopped 记录（#148）投影成 bot 看到的 update，附带拉黑
// 或解除拉黑的用户；它不涉及任何 chat。
func tgBotStoppedUpdate(update domain.BotUpdate) (tg.UpdateClass, []int64, tg.ChatClass) {
	s := update.BotStopped
	return &tg.UpdateBotStopped{
		UserID:  s.UserID,
		Date:    update.Date,
		Stopped: s.Stopped,
		Qts:     update.Qts,
	}, []int64{s.UserID}, nil
}
