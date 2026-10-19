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
		member ChannelMember
		want   ChannelMember
	}{
		{
			name:   "active restriction before expiry stays",
			member: ChannelMember{Status: ChannelMemberActive, BannedRights: mute(now + 1)},
			want:   ChannelMember{Status: ChannelMemberActive, BannedRights: mute(now + 1)},
		},
		{
			name:   "active restriction at expiry is lifted",
			member: ChannelMember{Status: ChannelMemberActive, BannedRights: mute(now)},
			want:   ChannelMember{Status: ChannelMemberActive},
		},
		{
			name:   "active restriction after expiry is lifted",
			member: ChannelMember{Status: ChannelMemberActive, BannedRights: mute(now - 1)},
			want:   ChannelMember{Status: ChannelMemberActive},
		},
		{
			name:   "permanent restriction never expires",
			member: ChannelMember{Status: ChannelMemberActive, BannedRights: mute(0)},
			want:   ChannelMember{Status: ChannelMemberActive, BannedRights: mute(0)},
		},
		{
			name:   "left member keeps an unexpired restriction",
			member: ChannelMember{Status: ChannelMemberLeft, BannedRights: mute(now + 60)},
			want:   ChannelMember{Status: ChannelMemberLeft, BannedRights: mute(now + 60)},
		},
		{
			name:   "restricted non-member after expiry is plain left",
			member: ChannelMember{Status: ChannelMemberBanned, BannedRights: mute(now - 1)},
			want:   ChannelMember{Status: ChannelMemberLeft},
		},
		{
			name:   "restricted non-member before expiry keeps status",
			member: ChannelMember{Status: ChannelMemberBanned, BannedRights: mute(now + 1)},
			want:   ChannelMember{Status: ChannelMemberBanned, BannedRights: mute(now + 1)},
		},
		{
			name:   "kick before expiry stays",
			member: ChannelMember{Status: ChannelMemberKicked, LeftAt: 7, BannedRights: kick(now + 1)},
			want:   ChannelMember{Status: ChannelMemberKicked, LeftAt: 7, BannedRights: kick(now + 1)},
		},
		{
			name:   "kick at expiry becomes left",
			member: ChannelMember{Status: ChannelMemberKicked, LeftAt: 7, BannedRights: kick(now)},
			want:   ChannelMember{Status: ChannelMemberLeft, LeftAt: 7},
		},
		{
			name:   "permanent kick never expires",
			member: ChannelMember{Status: ChannelMemberKicked, BannedRights: kick(0)},
			want:   ChannelMember{Status: ChannelMemberKicked, BannedRights: kick(0)},
		},
		{
			name:   "unrestricted member is untouched",
			member: ChannelMember{Status: ChannelMemberActive, Role: ChannelRoleAdmin},
			want:   ChannelMember{Status: ChannelMemberActive, Role: ChannelRoleAdmin},
		},
		{
			name:   "missing clock leaves the record as stored",
			member: ChannelMember{Status: ChannelMemberKicked, BannedRights: kick(5)},
			want:   ChannelMember{Status: ChannelMemberKicked, BannedRights: kick(5)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			at := now
			if tt.name == "missing clock leaves the record as stored" {
				at = 0
			}
			if got := tt.member.EffectiveAt(at); got != tt.want {
				t.Fatalf("EffectiveAt(%d) = %+v, want %+v", at, got, tt.want)
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
