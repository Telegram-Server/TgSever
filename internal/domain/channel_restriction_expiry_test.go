package domain

import "testing"

func TestChannelMemberEffectiveAt(t *testing.T) {
	const now = 1_800_000_000
	mute := func(until int) ChannelBannedRights {
		return ChannelBannedRights{SendMessages: true, SendMedia: true, UntilDate: until}
	}
	kick := func(until int) ChannelBannedRights {
		return ChannelBannedRights{ViewMessages: true, SendMessages: true, UntilDate: until}
	}
	tests := []struct {
		name   string
		at     int
		member ChannelMember
		want   ChannelMember
	}{
		{
			name:   "active restriction before expiry stays",
			at:     now,
			member: ChannelMember{Status: ChannelMemberActive, BannedRights: mute(now + 1)},
			want:   ChannelMember{Status: ChannelMemberActive, BannedRights: mute(now + 1)},
		},
		{
			name:   "active restriction at expiry is lifted",
			at:     now,
			member: ChannelMember{Status: ChannelMemberActive, BannedRights: mute(now)},
			want:   ChannelMember{Status: ChannelMemberActive},
		},
		{
			name:   "expired restriction drops its actor and date",
			at:     now,
			member: ChannelMember{Status: ChannelMemberActive, InviterUserID: 5, RestrictedBy: 7, RestrictedAt: now - 100, BannedRights: mute(now - 1)},
			want:   ChannelMember{Status: ChannelMemberActive, InviterUserID: 5},
		},
		{
			name:   "unexpired restriction keeps its actor and date",
			at:     now,
			member: ChannelMember{Status: ChannelMemberActive, InviterUserID: 5, RestrictedBy: 7, RestrictedAt: now - 100, BannedRights: mute(now + 1)},
			want:   ChannelMember{Status: ChannelMemberActive, InviterUserID: 5, RestrictedBy: 7, RestrictedAt: now - 100, BannedRights: mute(now + 1)},
		},
		{
			name:   "active restriction after expiry is lifted",
			at:     now,
			member: ChannelMember{Status: ChannelMemberActive, BannedRights: mute(now - 1)},
			want:   ChannelMember{Status: ChannelMemberActive},
		},
		{
			name:   "permanent restriction never expires",
			at:     now,
			member: ChannelMember{Status: ChannelMemberActive, BannedRights: mute(0)},
			want:   ChannelMember{Status: ChannelMemberActive, BannedRights: mute(0)},
		},
		{
			name:   "left member keeps an unexpired restriction",
			at:     now,
			member: ChannelMember{Status: ChannelMemberLeft, BannedRights: mute(now + 60)},
			want:   ChannelMember{Status: ChannelMemberLeft, BannedRights: mute(now + 60)},
		},
		{
			name:   "restricted non-member after expiry is plain left",
			at:     now,
			member: ChannelMember{Status: ChannelMemberBanned, BannedRights: mute(now - 1)},
			want:   ChannelMember{Status: ChannelMemberLeft},
		},
		{
			name:   "restricted non-member before expiry keeps status",
			at:     now,
			member: ChannelMember{Status: ChannelMemberBanned, BannedRights: mute(now + 1)},
			want:   ChannelMember{Status: ChannelMemberBanned, BannedRights: mute(now + 1)},
		},
		{
			name:   "kick before expiry stays",
			at:     now,
			member: ChannelMember{Status: ChannelMemberKicked, LeftAt: 7, BannedRights: kick(now + 1)},
			want:   ChannelMember{Status: ChannelMemberKicked, LeftAt: 7, BannedRights: kick(now + 1)},
		},
		{
			name:   "kick at expiry becomes left",
			at:     now,
			member: ChannelMember{Status: ChannelMemberKicked, LeftAt: 7, BannedRights: kick(now)},
			want:   ChannelMember{Status: ChannelMemberLeft, LeftAt: 7},
		},
		{
			name:   "permanent kick never expires",
			at:     now,
			member: ChannelMember{Status: ChannelMemberKicked, BannedRights: kick(0)},
			want:   ChannelMember{Status: ChannelMemberKicked, BannedRights: kick(0)},
		},
		{
			name:   "unrestricted member is untouched",
			at:     now,
			member: ChannelMember{Status: ChannelMemberActive, Role: ChannelRoleAdmin},
			want:   ChannelMember{Status: ChannelMemberActive, Role: ChannelRoleAdmin},
		},
		{
			name:   "missing clock leaves the record as stored",
			at:     0,
			member: ChannelMember{Status: ChannelMemberKicked, BannedRights: kick(5)},
			want:   ChannelMember{Status: ChannelMemberKicked, BannedRights: kick(5)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.member.EffectiveAt(tt.at); got != tt.want {
				t.Fatalf("EffectiveAt(%d) = %+v, want %+v", tt.at, got, tt.want)
			}
		})
	}
}

func TestChannelMemberIsKicked(t *testing.T) {
	tests := []struct {
		name   string
		member ChannelMember
		want   bool
	}{
		{"kicked status", ChannelMember{Status: ChannelMemberKicked}, true},
		{"view_messages ban", ChannelMember{Status: ChannelMemberActive, BannedRights: ChannelBannedRights{ViewMessages: true}}, true},
		{"restricted non-member may rejoin", ChannelMember{Status: ChannelMemberBanned, BannedRights: ChannelBannedRights{SendMessages: true}}, false},
		{"left", ChannelMember{Status: ChannelMemberLeft}, false},
		{"active restricted", ChannelMember{Status: ChannelMemberActive, BannedRights: ChannelBannedRights{SendMedia: true}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.member.IsKicked(); got != tt.want {
				t.Fatalf("IsKicked() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestChannelBannedRightsEmpty(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rights ChannelBannedRights
		want   bool
	}{
		{name: "zero value", rights: ChannelBannedRights{}, want: true},
		{name: "single flag", rights: ChannelBannedRights{SendMedia: true}, want: false},
		{name: "view messages", rights: ChannelBannedRights{ViewMessages: true}, want: false},
		{name: "default-restriction flag", rights: ChannelBannedRights{ManageLinkedPeers: true}, want: false},
		// 只看限制项：只带期限、不带任何限制项的记录没有限制任何事，算空。
		{name: "until date only", rights: ChannelBannedRights{UntilDate: 100}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rights.Empty(); got != tc.want {
				t.Fatalf("Empty() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestChannelMemberStanding(t *testing.T) {
	mute := ChannelBannedRights{SendMessages: true}
	for _, tc := range []struct {
		name   string
		member ChannelMember
		want   ChannelMemberStanding
	}{
		{"kicked status", ChannelMember{Status: ChannelMemberKicked, BannedRights: ChannelBannedRights{ViewMessages: true}}, ChannelStandingKicked},
		{"active record with view_messages ban", ChannelMember{Status: ChannelMemberActive, BannedRights: ChannelBannedRights{ViewMessages: true}}, ChannelStandingKicked},
		{"restricted non-member", ChannelMember{Status: ChannelMemberBanned, BannedRights: mute}, ChannelStandingRestrictedOutside},
		{"restricted in the group", ChannelMember{Status: ChannelMemberActive, BannedRights: mute}, ChannelStandingRestricted},
		{"until date only in the group", ChannelMember{Status: ChannelMemberActive, BannedRights: ChannelBannedRights{UntilDate: 100}}, ChannelStandingMember},
		{"left", ChannelMember{Status: ChannelMemberLeft}, ChannelStandingLeft},
		{"left admin keeps its role but is left", ChannelMember{Status: ChannelMemberLeft, Role: ChannelRoleAdmin}, ChannelStandingLeft},
		{"plain member", ChannelMember{Status: ChannelMemberActive}, ChannelStandingMember},
		{"creator", ChannelMember{Status: ChannelMemberActive, Role: ChannelRoleCreator}, ChannelStandingMember},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.member.Standing(); got != tc.want {
				t.Fatalf("Standing() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestChannelAdminLogBanType(t *testing.T) {
	active := ChannelMember{Status: ChannelMemberActive}
	kicked := ChannelMember{Status: ChannelMemberKicked, BannedRights: ChannelBannedRights{ViewMessages: true}}
	muted := ChannelMember{Status: ChannelMemberActive, BannedRights: ChannelBannedRights{SendMessages: true}}
	outside := ChannelMember{Status: ChannelMemberBanned, BannedRights: ChannelBannedRights{SendMessages: true}}
	left := ChannelMember{Status: ChannelMemberLeft}
	for _, tc := range []struct {
		name           string
		previous, next ChannelMember
		want           ChannelAdminLogEventType
	}{
		{"kick", active, kicked, ChannelAdminLogParticipantKick},
		{"kick a restricted member", muted, kicked, ChannelAdminLogParticipantKick},
		{"unkick", kicked, left, ChannelAdminLogParticipantUnkick},
		{"unkick into a restriction", kicked, outside, ChannelAdminLogParticipantUnkick},
		{"restrict in the group", active, muted, ChannelAdminLogParticipantBan},
		{"restrict a non-member", left, outside, ChannelAdminLogParticipantBan},
		{"lift a restriction", muted, active, ChannelAdminLogParticipantUnban},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ChannelAdminLogBanType(tc.previous, tc.next); got != tc.want {
				t.Fatalf("ChannelAdminLogBanType = %v, want %v", got, tc.want)
			}
		})
	}
}

// ApplyBannedRights 是 channels.editBanned 对成员记录的改写：限制项决定状态，期限只随限制保存；
// 执行人与时间单独记录，邀请人不动。
func TestChannelMemberApplyBannedRights(t *testing.T) {
	const actor, inviter, date = int64(7), int64(5), 1_000
	mute := ChannelBannedRights{SendMessages: true, UntilDate: 2_000}
	kick := ChannelBannedRights{ViewMessages: true}
	for _, tc := range []struct {
		name     string
		previous ChannelMember
		rights   ChannelBannedRights
		want     ChannelMember
	}{
		{
			name:     "kick an active member",
			previous: ChannelMember{InviterUserID: inviter, Role: ChannelRoleAdmin, Status: ChannelMemberActive, JoinedAt: 10},
			rights:   kick,
			want:     ChannelMember{InviterUserID: inviter, Role: ChannelRoleMember, Status: ChannelMemberKicked, JoinedAt: 10, LeftAt: date, BannedRights: kick, RestrictedBy: actor, RestrictedAt: date},
		},
		{
			name:     "restrict an active member keeps them in the group",
			previous: ChannelMember{InviterUserID: inviter, Status: ChannelMemberActive, JoinedAt: 10},
			rights:   mute,
			want:     ChannelMember{InviterUserID: inviter, Role: ChannelRoleMember, Status: ChannelMemberActive, JoinedAt: 10, BannedRights: mute, RestrictedBy: actor, RestrictedAt: date},
		},
		{
			name:     "restrict a non-member",
			previous: ChannelMember{Status: ChannelMemberLeft, LeftAt: 50},
			rights:   mute,
			want:     ChannelMember{Role: ChannelRoleMember, Status: ChannelMemberBanned, LeftAt: 50, BannedRights: mute, RestrictedBy: actor, RestrictedAt: date},
		},
		{
			name:     "lift a restriction of an active member",
			previous: ChannelMember{InviterUserID: inviter, Status: ChannelMemberActive, JoinedAt: 10, BannedRights: mute, RestrictedBy: 3, RestrictedAt: 20},
			rights:   ChannelBannedRights{},
			want:     ChannelMember{InviterUserID: inviter, Role: ChannelRoleMember, Status: ChannelMemberActive, JoinedAt: 10},
		},
		{
			name:     "unkick leaves the user outside",
			previous: ChannelMember{Status: ChannelMemberKicked, LeftAt: 30, BannedRights: kick, RestrictedBy: 3, RestrictedAt: 30},
			rights:   ChannelBannedRights{},
			want:     ChannelMember{Role: ChannelRoleMember, Status: ChannelMemberLeft},
		},
		{
			name:     "rights with only an until date lift the restriction",
			previous: ChannelMember{InviterUserID: inviter, Status: ChannelMemberActive, JoinedAt: 10, BannedRights: mute, RestrictedBy: 3, RestrictedAt: 20},
			rights:   ChannelBannedRights{UntilDate: 5_000},
			want:     ChannelMember{InviterUserID: inviter, Role: ChannelRoleMember, Status: ChannelMemberActive, JoinedAt: 10},
		},
		{
			name:     "until date only on a restricted non-member makes them plain left",
			previous: ChannelMember{Status: ChannelMemberBanned, LeftAt: 50, BannedRights: mute, RestrictedBy: 3, RestrictedAt: 20},
			rights:   ChannelBannedRights{UntilDate: 5_000},
			want:     ChannelMember{Role: ChannelRoleMember, Status: ChannelMemberLeft},
		},
		{
			name:     "active member without a join date gets the restriction date",
			previous: ChannelMember{Status: ChannelMemberActive},
			rights:   mute,
			want:     ChannelMember{Role: ChannelRoleMember, Status: ChannelMemberActive, JoinedAt: date, BannedRights: mute, RestrictedBy: actor, RestrictedAt: date},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.previous.ApplyBannedRights(tc.rights, actor, date); got != tc.want {
				t.Fatalf("ApplyBannedRights = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// 重新入群时旧记录的限制连同执行人与时间一起带回；被踢出者入群等同解封；到期的限制不带回。
func TestChannelMemberCarryRejoinRestriction(t *testing.T) {
	const now = 1_000
	mute := ChannelBannedRights{SendMessages: true}
	for _, tc := range []struct {
		name     string
		existing ChannelMember
		want     ChannelMember
	}{
		{
			name:     "a restriction survives leaving and rejoining",
			existing: ChannelMember{Status: ChannelMemberLeft, BannedRights: mute, RestrictedBy: 7, RestrictedAt: 500},
			want:     ChannelMember{Status: ChannelMemberActive, BannedRights: mute, RestrictedBy: 7, RestrictedAt: 500},
		},
		{
			name:     "a restricted non-member brings the restriction",
			existing: ChannelMember{Status: ChannelMemberBanned, BannedRights: mute, RestrictedBy: 7, RestrictedAt: 500},
			want:     ChannelMember{Status: ChannelMemberActive, BannedRights: mute, RestrictedBy: 7, RestrictedAt: 500},
		},
		{
			name:     "a kicked user let back in is unbanned",
			existing: ChannelMember{Status: ChannelMemberKicked, BannedRights: ChannelBannedRights{ViewMessages: true}, RestrictedBy: 7, RestrictedAt: 500},
			want:     ChannelMember{Status: ChannelMemberActive},
		},
		{
			name:     "an expired restriction is not brought back",
			existing: ChannelMember{Status: ChannelMemberLeft, BannedRights: ChannelBannedRights{SendMessages: true, UntilDate: now}, RestrictedBy: 7, RestrictedAt: 500},
			want:     ChannelMember{Status: ChannelMemberActive},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			member := ChannelMember{Status: ChannelMemberActive, BannedRights: ChannelBannedRights{SendMedia: true}, RestrictedBy: 99, RestrictedAt: 99}
			member.CarryRejoinRestriction(tc.existing, now)
			if member != tc.want {
				t.Fatalf("CarryRejoinRestriction = %+v, want %+v", member, tc.want)
			}
		})
	}
}
