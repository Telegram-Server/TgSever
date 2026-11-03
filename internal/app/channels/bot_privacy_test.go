package channels

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// testBotDirectory 在 testBotProfiles 之上补 bot 用户名，供 /cmd@username 归属判定。
type testBotDirectory struct {
	testBotProfiles
	usernames map[int64]string
}

func (d testBotDirectory) BotUsername(_ context.Context, botUserID int64) (string, bool, error) {
	username, ok := d.usernames[botUserID]
	return username, ok, nil
}

const (
	privacyTestCreator    int64 = 1001
	privacyTestHuman      int64 = 1002
	privacyTestMemberBot  int64 = 1003 // privacy mode 开，普通成员
	privacyTestAdminBot   int64 = 1004 // privacy mode 开，群管理员
	privacyTestHistoryBot int64 = 1005 // privacy mode 关
)

func newBotPrivacyTestGroup(t *testing.T) (*Service, domain.CreateChannelResult) {
	t.Helper()
	ctx := context.Background()
	bots := testBotDirectory{
		testBotProfiles: testBotProfiles{
			privacyTestMemberBot:  {BotUserID: privacyTestMemberBot},
			privacyTestAdminBot:   {BotUserID: privacyTestAdminBot},
			privacyTestHistoryBot: {BotUserID: privacyTestHistoryBot, ChatHistory: true},
		},
		usernames: map[int64]string{
			privacyTestMemberBot:  "member_bot",
			privacyTestAdminBot:   "admin_bot",
			privacyTestHistoryBot: "history_bot",
		},
	}
	service := NewService(memory.NewChannelStore(), WithBotProfileResolver(bots))
	created, err := service.CreateMegagroupFromCreateChat(ctx, privacyTestCreator, domain.CreateChannelRequest{
		Title:         "Privacy",
		MemberUserIDs: []int64{privacyTestHuman, privacyTestMemberBot, privacyTestAdminBot, privacyTestHistoryBot},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	if _, err := service.EditAdmin(ctx, privacyTestCreator, domain.EditChannelAdminRequest{
		ChannelID: created.Channel.ID, MemberID: privacyTestAdminBot, AdminRights: domain.ChannelAdminRights{PinMessages: true}, Date: 11,
	}); err != nil {
		t.Fatalf("promote admin bot: %v", err)
	}
	return service, created
}

// TestPrivacyBotVisibilityMatrix 覆盖 privacy mode 的放行规则：群管理员 bot 与关了 privacy mode 的 bot
// 看到全部消息；普通成员 bot 只看到服务消息、发给它的命令、经它 inline 发出的消息及其回复。
// 实时投递（Recipients）、getHistory、getChannelDifference 三条路径结论必须一致。
func TestPrivacyBotVisibilityMatrix(t *testing.T) {
	ctx := context.Background()
	service, created := newBotPrivacyTestGroup(t)
	channelID := created.Channel.ID
	startPts := created.Event.Pts

	send := func(randomID int64, req domain.SendChannelMessageRequest) domain.SendChannelMessageResult {
		t.Helper()
		req.ChannelID = channelID
		req.RandomID = randomID
		req.Date = 20 + int(randomID%100)
		res, err := service.SendMessage(ctx, privacyTestHuman, req)
		if err != nil {
			t.Fatalf("SendMessage %q: %v", req.Message, err)
		}
		return res
	}

	plain := send(2001, domain.SendChannelMessageRequest{Message: "plain text"})
	toOther := send(2002, domain.SendChannelMessageRequest{Message: "/status@admin_bot now"})
	toMember := send(2003, domain.SendChannelMessageRequest{Message: "/status@Member_Bot"})
	bare := send(2004, domain.SendChannelMessageRequest{Message: "/status"})
	viaBot := send(2005, domain.SendChannelMessageRequest{Message: "inline result", ViaBotID: privacyTestMemberBot})
	replyVia := send(2006, domain.SendChannelMessageRequest{Message: "reply to inline", ReplyTo: &domain.MessageReply{MessageID: viaBot.Message.ID}})
	retitled, err := service.EditTitle(ctx, privacyTestCreator, domain.EditChannelTitleRequest{ChannelID: channelID, Title: "Privacy 2", Date: 40})
	if err != nil {
		t.Fatalf("EditTitle: %v", err)
	}

	cases := []struct {
		name          string
		res           domain.SendChannelMessageResult
		memberBotSees bool
	}{
		{"plain text", plain, false},
		{"command for another bot", toOther, false},
		{"command for this bot", toMember, true},
		{"command without suffix", bare, true},
		{"via this bot", viaBot, true},
		{"reply to via-bot message", replyVia, true},
	}
	for _, tc := range cases {
		if got := testContainsInt64(tc.res.Recipients, privacyTestMemberBot); got != tc.memberBotSees {
			t.Errorf("%s: member bot in recipients = %v, want %v", tc.name, got, tc.memberBotSees)
		}
		for _, bot := range []int64{privacyTestAdminBot, privacyTestHistoryBot} {
			if !testContainsInt64(tc.res.Recipients, bot) {
				t.Errorf("%s: recipients = %v, want exempt bot %d", tc.name, tc.res.Recipients, bot)
			}
		}
	}

	all := []string{"plain text", "/status@admin_bot now", "/status@Member_Bot", "/status", "inline result", "reply to inline"}
	memberVisible := []string{"/status@Member_Bot", "/status", "inline result", "reply to inline"}
	for _, check := range []struct {
		bot  int64
		want []string
	}{
		{privacyTestMemberBot, memberVisible},
		{privacyTestAdminBot, all},
		{privacyTestHistoryBot, all},
	} {
		history, err := service.GetHistory(ctx, check.bot, domain.ChannelHistoryFilter{ChannelID: channelID, Limit: 50})
		if err != nil {
			t.Fatalf("GetHistory bot %d: %v", check.bot, err)
		}
		assertBotSeesTexts(t, "history", check.bot, history.Messages, check.want)
		assertBotSeesService(t, "history", check.bot, history.Messages, retitled.Message.ID)

		diff, err := service.GetDifference(ctx, check.bot, domain.ChannelDifferenceRequest{ChannelID: channelID, Pts: startPts, Limit: 50})
		if err != nil {
			t.Fatalf("GetDifference bot %d: %v", check.bot, err)
		}
		assertBotSeesTexts(t, "difference", check.bot, diff.NewMessages, check.want)
		assertBotSeesService(t, "difference", check.bot, diff.NewMessages, retitled.Message.ID)
	}
}

// TestPrivacyBotEditFollowsAdminRole 覆盖编辑推送与角色变化：管理员 bot 收到普通消息的编辑；
// 撤销管理员后不再缓存旧角色，立即回到 privacy 规则。
func TestPrivacyBotEditFollowsAdminRole(t *testing.T) {
	ctx := context.Background()
	service, created := newBotPrivacyTestGroup(t)
	channelID := created.Channel.ID

	sent, err := service.SendMessage(ctx, privacyTestHuman, domain.SendChannelMessageRequest{ChannelID: channelID, RandomID: 3001, Message: "before", Date: 20})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	edited, err := service.EditMessage(ctx, privacyTestHuman, domain.EditChannelMessageRequest{ChannelID: channelID, ID: sent.Message.ID, Message: "after"})
	if err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	if !testContainsInt64(edited.Recipients, privacyTestAdminBot) || testContainsInt64(edited.SkipDeliveryUserIDs, privacyTestAdminBot) {
		t.Fatalf("edit recipients = %v skip = %v, want admin bot delivered", edited.Recipients, edited.SkipDeliveryUserIDs)
	}
	if testContainsInt64(edited.Recipients, privacyTestMemberBot) || !testContainsInt64(edited.SkipDeliveryUserIDs, privacyTestMemberBot) {
		t.Fatalf("edit recipients = %v skip = %v, want member bot skipped", edited.Recipients, edited.SkipDeliveryUserIDs)
	}

	if _, err := service.EditAdmin(ctx, privacyTestCreator, domain.EditChannelAdminRequest{ChannelID: channelID, MemberID: privacyTestAdminBot, Date: 22}); err != nil {
		t.Fatalf("demote admin bot: %v", err)
	}
	demoted, err := service.SendMessage(ctx, privacyTestHuman, domain.SendChannelMessageRequest{ChannelID: channelID, RandomID: 3002, Message: "after demote", Date: 23})
	if err != nil {
		t.Fatalf("SendMessage after demote: %v", err)
	}
	if testContainsInt64(demoted.Recipients, privacyTestAdminBot) {
		t.Fatalf("recipients after demote = %v, demoted bot must fall back to privacy mode", demoted.Recipients)
	}
	history, err := service.GetHistory(ctx, privacyTestAdminBot, domain.ChannelHistoryFilter{ChannelID: channelID, Limit: 50})
	if err != nil {
		t.Fatalf("GetHistory after demote: %v", err)
	}
	for _, msg := range history.Messages {
		if msg.Body == "after demote" || msg.Body == "after" {
			t.Fatalf("demoted bot history leaked %q", msg.Body)
		}
	}
}

func TestMessageCommandTarget(t *testing.T) {
	cases := []struct {
		in        string
		isCommand bool
		target    string
	}{
		{"/start", true, ""},
		{"  /start arg", true, ""},
		{"/start@my_bot", true, "my_bot"},
		{"/start@my_bot arg@x", true, "my_bot"},
		{"/", false, ""},
		{"hello /start@my_bot", false, ""},
		{"", false, ""},
	}
	for _, tc := range cases {
		isCommand, target := messageCommandTarget(tc.in)
		if isCommand != tc.isCommand || target != tc.target {
			t.Errorf("messageCommandTarget(%q) = %v %q, want %v %q", tc.in, isCommand, target, tc.isCommand, tc.target)
		}
	}
}

func assertBotSeesTexts(t *testing.T, path string, bot int64, msgs []domain.ChannelMessage, want []string) {
	t.Helper()
	got := map[string]bool{}
	for _, msg := range msgs {
		if msg.Action == nil {
			got[msg.Body] = true
		}
	}
	for _, body := range want {
		if !got[body] {
			t.Errorf("%s bot %d: missing %q (got %v)", path, bot, body, got)
		}
		delete(got, body)
	}
	for body := range got {
		t.Errorf("%s bot %d: unexpected %q", path, bot, body)
	}
}

func assertBotSeesService(t *testing.T, path string, bot int64, msgs []domain.ChannelMessage, serviceID int) {
	t.Helper()
	for _, msg := range msgs {
		if msg.ID == serviceID && msg.Action != nil {
			return
		}
	}
	t.Errorf("%s bot %d: missing service message %d", path, bot, serviceID)
}
