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
	m.ClearRestriction()
	if m.Status == ChannelMemberKicked || m.Status == ChannelMemberBanned {
		m.Status = ChannelMemberLeft
	}
	return m
}

// Empty 报告限制记录是否没有任何限制项。只看限制项、不看 UntilDate：只带到期时间的记录没有
// 限制任何事。转换层判断"在群受限"、两个 store 判断"解除限制"都用它。
func (r ChannelBannedRights) Empty() bool {
	r.UntilDate = 0
	return r == ChannelBannedRights{}
}

// SetRestriction 记下当前限制（含踢出）由管理员 actor 在 at 时刻设置。
func (m *ChannelMember) SetRestriction(actor int64, at int) {
	m.RestrictedBy, m.RestrictedAt = actor, at
}

// ClearRestriction 清掉限制的执行人与时间；限制为空时两项必须为 0。
func (m *ChannelMember) ClearRestriction() {
	m.RestrictedBy, m.RestrictedAt = 0, 0
}

// ApplyBannedRights 返回管理员 actor 在 date 时刻对成员记录 m（已按 date 换算期限的视图）设置
// 限制 rights 后的记录，即 channels.editBanned 的写入：
//   - 带 view_messages：踢出（kicked，离开时间为 date）；
//   - 没有任何限制项（只带到期时间也算）：解除限制，不留执行人、时间与期限；在群的仍在群，
//     不在群的成为普通的 left；
//   - 其它：在群受限，或不在群里但受限（banned）。
//
// 角色一律降为普通成员；执行人与限制时间单独记录，邀请人不动。在群而没有入群时间的记录补上 date。
func (m ChannelMember) ApplyBannedRights(rights ChannelBannedRights, actor int64, date int) ChannelMember {
	wasActive := m.Status == ChannelMemberActive
	m.Role = ChannelRoleMember
	switch {
	case rights.ViewMessages:
		m.BannedRights = rights
		m.SetRestriction(actor, date)
		m.Status = ChannelMemberKicked
		m.LeftAt = date
	case rights.Empty():
		m.BannedRights = ChannelBannedRights{}
		m.ClearRestriction()
		if !wasActive {
			m.Status = ChannelMemberLeft
		}
		m.LeftAt = 0
	default:
		m.BannedRights = rights
		m.SetRestriction(actor, date)
		if !wasActive {
			m.Status = ChannelMemberBanned
		}
	}
	if m.JoinedAt == 0 && m.Status == ChannelMemberActive {
		m.JoinedAt = date
	}
	return m
}

// CarryRejoinRestriction 把旧成员记录 existing 重新入群后应带的限制写进 m：退群不解除限制，
// 否则"先退群再让人拉回来"就能绕过；不在群里时被限制的人（Status banned）同样带着限制入群。
// 被踢出者（IsKicked）能重新入群只可能是被有封禁权的管理员拉回，等同解封。
// 期限在入群时刻 now 已过的限制视为无限制。带回的限制连同执行人与限制时间一起带回；
// 不带限制时两项为 0。
func (m *ChannelMember) CarryRejoinRestriction(existing ChannelMember, now int) {
	existing = existing.EffectiveAt(now)
	if existing.IsKicked() || existing.BannedRights.Empty() {
		m.BannedRights = ChannelBannedRights{}
		m.ClearRestriction()
		return
	}
	m.BannedRights = existing.BannedRights
	m.SetRestriction(existing.RestrictedBy, existing.RestrictedAt)
}

// ChannelMemberStanding 是成员记录在群里的处境，成员类型输出、成员名单筛选、管理日志的
// 封禁类事件都按它分类。
type ChannelMemberStanding uint8

const (
	// ChannelStandingMember：在群且没有限制，按角色（creator / admin / 普通成员）看待。
	ChannelStandingMember ChannelMemberStanding = iota
	// ChannelStandingKicked：被踢出或带 view_messages 禁令。
	ChannelStandingKicked
	// ChannelStandingRestrictedOutside：不在群里但受限（Status banned）。
	ChannelStandingRestrictedOutside
	// ChannelStandingRestricted：在群受限（在群且有限制项）。
	ChannelStandingRestricted
	// ChannelStandingLeft：已离开，没有限制。
	ChannelStandingLeft
)

// Standing 给成员记录分类，判断顺序：被踢出 → 不在群里但受限 → 在群受限 → 已离开 → 正常。
// 调用方应先用 EffectiveAt 换算期限。
func (m ChannelMember) Standing() ChannelMemberStanding {
	switch {
	case m.IsKicked():
		return ChannelStandingKicked
	case m.Status == ChannelMemberBanned:
		return ChannelStandingRestrictedOutside
	case m.Status == ChannelMemberActive && !m.BannedRights.Empty():
		return ChannelStandingRestricted
	case m.Status == ChannelMemberLeft:
		return ChannelStandingLeft
	default:
		return ChannelStandingMember
	}
}

// IsRestricted 报告处境是否为受限（在群受限或不在群里但受限），即受限名单收录的记录。
func (s ChannelMemberStanding) IsRestricted() bool {
	return s == ChannelStandingRestricted || s == ChannelStandingRestrictedOutside
}

// ChannelAdminLogBanType 返回一次限制变更（previous → next）在管理日志里的事件类型：
// 踢出、解除踢出、设置限制、解除限制。
func ChannelAdminLogBanType(previous, next ChannelMember) ChannelAdminLogEventType {
	switch {
	case next.Standing() == ChannelStandingKicked:
		return ChannelAdminLogParticipantKick
	case previous.Standing() == ChannelStandingKicked:
		return ChannelAdminLogParticipantUnkick
	case next.Standing().IsRestricted():
		return ChannelAdminLogParticipantBan
	default:
		return ChannelAdminLogParticipantUnban
	}
}

// IsKicked 报告记录是否表示被踢出/封禁（view_messages 禁令）：这样的用户看不到频道、
// 不能自行加入，也不能被没有封禁权的管理员拉回。只有部分限制的记录（含 Status banned，
// 即不在群里但带着限制）不算。调用方应先用 EffectiveAt 换算期限。
func (m ChannelMember) IsKicked() bool {
	return m.Status == ChannelMemberKicked || m.BannedRights.ViewMessages
}
