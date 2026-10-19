package botapi

import (
	"testing"

	"telesrv/internal/domain"
)

func TestParseChatPermissionsTreatsOmittedFieldsAsForbidden(t *testing.T) {
	p, err := parseChatPermissions(`{"can_send_messages":true,"can_send_photos":true}`)
	if err != nil {
		t.Fatalf("parseChatPermissions: %v", err)
	}
	got := chatPermissionsToBanned(p, true)
	// Photos are still allowed, so the send_media master switch stays off: it is
	// only set once every granular media right is banned.
	want := domain.ChannelBannedRights{
		SendStickers:    true,
		SendGifs:        true,
		SendGames:       true,
		SendInline:      true,
		EmbedLinks:      true,
		SendPolls:       true,
		ChangeInfo:      true,
		InviteUsers:     true,
		PinMessages:     true,
		ManageTopics:    true,
		SendVideos:      true,
		SendRoundvideos: true,
		SendAudios:      true,
		SendVoices:      true,
		SendDocs:        true,
	}
	if got != want {
		t.Fatalf("banned = %+v\nwant   %+v", got, want)
	}
}

func TestChatPermissionsAllForbiddenBansSendingEntirely(t *testing.T) {
	p, err := parseChatPermissions(`{}`)
	if err != nil {
		t.Fatalf("parseChatPermissions: %v", err)
	}
	got := chatPermissionsToBanned(p, true)
	if !got.SendMessages || !got.SendPlain || !got.SendMedia || got.ViewMessages {
		t.Fatalf("banned = %+v, want every send right banned while the member can still read", got)
	}
}

func TestDependentPermissionsImplyCanSendMessages(t *testing.T) {
	// Without use_independent_chat_permissions, the official gateway treats any
	// media or poll right as also granting plain messages.
	p, err := parseChatPermissions(`{"can_send_polls":true}`)
	if err != nil {
		t.Fatalf("parseChatPermissions: %v", err)
	}
	if dependent := chatPermissionsToBanned(p, false); dependent.SendPlain || dependent.SendMessages {
		t.Fatalf("dependent banned = %+v, want plain messages allowed", dependent)
	}
	if independent := chatPermissionsToBanned(p, true); !independent.SendPlain {
		t.Fatalf("independent banned = %+v, want plain messages still banned", independent)
	}
}

func TestParseChatPermissionsRejectsMalformedJSON(t *testing.T) {
	if _, err := parseChatPermissions(`not json`); err == nil {
		t.Fatal("parseChatPermissions accepted malformed JSON")
	}
}

func TestAPIChatPermissionsRendersBannedRightsAsGrants(t *testing.T) {
	got := apiChatPermissions(domain.ChannelBannedRights{SendPolls: true, PinMessages: true, SendStickers: true})
	want := map[string]bool{
		"can_send_messages":         true,
		"can_send_audios":           true,
		"can_send_documents":        true,
		"can_send_photos":           true,
		"can_send_videos":           true,
		"can_send_video_notes":      true,
		"can_send_voice_notes":      true,
		"can_send_polls":            false,
		"can_send_other_messages":   false,
		"can_add_web_page_previews": true,
		"can_change_info":           true,
		"can_invite_users":          true,
		"can_pin_messages":          false,
		"can_manage_topics":         true,
	}
	if len(got) != len(want) {
		t.Fatalf("permissions = %+v, want exactly %d fields", got, len(want))
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("%s = %v, want %v (all: %+v)", key, got[key], value, got)
		}
	}
}

func TestAPIChatPermissionsMasterBanForbidsEveryMessageKind(t *testing.T) {
	got := apiChatPermissions(domain.ChannelBannedRights{SendMessages: true})
	for _, key := range []string{"can_send_messages", "can_send_photos", "can_send_polls", "can_send_other_messages", "can_add_web_page_previews"} {
		if got[key] != false {
			t.Fatalf("%s = %v under send_messages ban, want false", key, got[key])
		}
	}
	if got["can_invite_users"] != true {
		t.Fatalf("can_invite_users = %v, want unaffected by the send ban", got["can_invite_users"])
	}
}

func TestAdminRightsFromParamsReadsOfficialFlags(t *testing.T) {
	got := adminRightsFromParams(map[string]string{
		"can_manage_chat":        "true",
		"can_delete_messages":    "true",
		"can_restrict_members":   "true",
		"can_promote_members":    "false",
		"can_pin_messages":       "true",
		"can_manage_video_chats": "true",
		"is_anonymous":           "true",
		"unrelated":              "true",
	})
	want := domain.ChannelAdminRights{
		ManageChat:     true,
		DeleteMessages: true,
		BanUsers:       true,
		PinMessages:    true,
		ManageCall:     true,
		Anonymous:      true,
	}
	if got != want {
		t.Fatalf("rights = %+v\nwant   %+v", got, want)
	}
}

func TestAPIAdministratorRightsRoundTripsFlags(t *testing.T) {
	rights := domain.ChannelAdminRights{ChangeInfo: true, InviteUsers: true, AddAdmins: true, ManageTopics: true, PostStories: true}
	got := apiAdministratorRights(rights)
	if got["can_change_info"] != true || got["can_invite_users"] != true || got["can_promote_members"] != true ||
		got["can_manage_topics"] != true || got["can_post_stories"] != true || got["can_delete_messages"] != false ||
		got["is_anonymous"] != false {
		t.Fatalf("rights = %+v", got)
	}
	params := map[string]string{}
	for key, value := range got {
		if value {
			params[key] = "true"
		}
	}
	if back := adminRightsFromParams(params); back != rights {
		t.Fatalf("round trip = %+v, want %+v", back, rights)
	}
}

func TestNormalizeUntilDateFollowsOfficialForeverWindow(t *testing.T) {
	const now = 1_000_000
	for _, tc := range []struct {
		raw, want int
	}{
		{0, 0},
		{now + 10, 0},            // under 30 seconds: forever
		{now + 3600, now + 3600}, // ordinary temporary restriction
		{now + 367*86400, 0},     // more than 366 days: forever
		{now - 5, 0},             // already in the past: forever
		{now + 366*86400, now + 366*86400},
	} {
		if got := normalizeUntilDate(tc.raw, now); got != tc.want {
			t.Fatalf("normalizeUntilDate(%d) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}
