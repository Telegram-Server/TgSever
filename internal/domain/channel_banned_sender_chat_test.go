package domain

import "testing"

func TestSenderChatBanAdminLogEvent(t *testing.T) {
	req := EditChannelBannedSenderChatRequest{UserID: 7, ChannelID: 100, SenderChatID: 200, Date: 1}
	record := ChannelBannedSenderChat{ChannelID: 100, SenderChatID: 200, BannedByUserID: 7, Date: 50}

	ban := SenderChatBanAdminLogEvent(req, record, true, 60)
	if ban.Type != ChannelAdminLogParticipantKick || ban.ChannelID != 100 || ban.UserID != 7 || ban.Date != 60 {
		t.Fatalf("ban event = %+v", ban)
	}
	if ban.PrevParticipant == nil || ban.PrevParticipant.Status != ChannelMemberLeft || ban.PrevParticipant.SenderChatID != 200 ||
		ban.NewParticipant == nil || ban.NewParticipant.Status != ChannelMemberKicked || ban.NewParticipant.SenderChatID != 200 || !ban.NewParticipant.BannedRights.ViewMessages {
		t.Fatalf("ban participants prev=%+v new=%+v", ban.PrevParticipant, ban.NewParticipant)
	}

	unban := SenderChatBanAdminLogEvent(req, record, false, 70)
	if unban.Type != ChannelAdminLogParticipantUnkick || unban.Date != 70 ||
		unban.PrevParticipant == nil || unban.PrevParticipant.Status != ChannelMemberKicked ||
		unban.NewParticipant == nil || unban.NewParticipant.Status != ChannelMemberLeft {
		t.Fatalf("unban event = %+v prev=%+v new=%+v", unban, unban.PrevParticipant, unban.NewParticipant)
	}
}
