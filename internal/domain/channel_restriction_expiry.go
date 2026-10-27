package domain

// 成员限制的期限语义。
//
// ChannelBannedRights.UntilDate 为 0 表示永久；> 0 时到该时刻（含）限制失效。存储层保存的是
// 管理员设置时的原始记录，读取时一律经 EffectiveAt 换算成"当前生效"的视图，过期限制
// 不必真正从库里清掉。
//
// Status 的含义：
//   - kicked：被踢出/封禁（总是带 view_messages），不能自行重新加入。
//   - banned：不在群里但带着部分限制（官方 channelParticipantBanned left=true）。它不阻止
//     入群，重新入群后限制保留；只有 IsKicked 的记录才阻止入群。

// EffectiveAt 返回成员记录在 now 时刻实际生效的视图：期限已到（UntilDate > 0 且 <= now）
// 的限制视为无限制（执行人与限制时间随之清零）；被踢出者到期视为已解封（left，可重新加入）；不在群里的受限者到期
// 视为普通的 left。now <= 0（调用方没有时钟）时原样返回。
func (m ChannelMember) EffectiveAt(now int) ChannelMember {
	until := m.BannedRights.UntilDate
	if now <= 0 || until <= 0 || until > now {
		return m
	}
	m.BannedRights = ChannelBannedRights{}
	m.RestrictedBy, m.RestrictedAt = 0, 0
	if m.Status == ChannelMemberKicked || m.Status == ChannelMemberBanned {
		m.Status = ChannelMemberLeft
	}
	return m
}

// Empty 报告限制记录是否为空：没有任何限制项，也没有期限。转换层判断"在群受限"、两个
// store 判断"解除限制"都用它。
func (r ChannelBannedRights) Empty() bool {
	return r == ChannelBannedRights{}
}

// IsKicked 报告记录是否表示被踢出/封禁（view_messages 禁令）：这样的用户看不到频道、
// 不能自行加入，也不能被没有封禁权的管理员拉回。只有部分限制的记录（含 Status banned，
// 即不在群里但带着限制）不算。调用方应先用 EffectiveAt 换算期限。
func (m ChannelMember) IsKicked() bool {
	return m.Status == ChannelMemberKicked || m.BannedRights.ViewMessages
}
