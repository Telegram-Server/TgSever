package store

import "telesrv/internal/domain"

// BlocklistBotUpdates 把一次拉黑名单变动里对 bot 的拉黑/解除拉黑转成 updateBotStopped
// 记录（#148）。只看 Changes（拉黑状态确实变化的对象），bot 身份取自同一事务冻结的
// Peers；拉黑普通用户不生成记录。
func BlocklistBotUpdates(s BlocklistMutationSnapshot) []domain.BotUpdate {
	if len(s.Changes) == 0 || len(s.Peers) != len(s.Changes) {
		return nil
	}
	var out []domain.BotUpdate
	for i, c := range s.Changes {
		if f := s.Peers[i]; f.PeerUserID != c.PeerUserID || !f.Bot {
			continue
		}
		out = append(out, domain.BotStoppedChange(s.OwnerUserID, c.PeerUserID, c.Blocked, s.Date))
	}
	return out
}
