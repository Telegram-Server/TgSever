package rpc

import (
	"strings"
	"testing"

	"telesrv/internal/domain"
)

func TestBotAPISetBotInfoNameIsReadBackAndSeenByOthers(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	ok, err := f.router.BotAPISetBotInfo(f.ctx, f.bot.ID, "", domain.BotInfoUpdate{SetName: true, Name: "Renamed Bot"})
	if err != nil || !ok {
		t.Fatalf("set name = %v, %v", ok, err)
	}
	name, _, _, err := f.router.BotAPIGetBotInfo(f.ctx, f.bot.ID, "")
	if err != nil || name != "Renamed Bot" {
		t.Fatalf("get name = %q, %v", name, err)
	}
	seen, found, err := f.users.ByID(f.ctx, f.member.ID, f.bot.ID)
	if err != nil || !found || seen.FirstName != "Renamed Bot" {
		t.Fatalf("member sees bot as %q (found=%v, err=%v)", seen.FirstName, found, err)
	}
}

// 默认语言的名字不能清空：处理器把空名字当作 BOT_INVALID。
func TestBotAPISetBotInfoRejectsEmptyName(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	_, err := f.router.BotAPISetBotInfo(f.ctx, f.bot.ID, "", domain.BotInfoUpdate{SetName: true, Name: "  "})
	if err == nil || !strings.Contains(err.Error(), "BOT_INVALID") {
		t.Fatalf("set empty name err = %v, want BOT_INVALID", err)
	}
	name, _, _, err := f.router.BotAPIGetBotInfo(f.ctx, f.bot.ID, "")
	if err != nil || name != "BridgeBot" {
		t.Fatalf("name after rejected set = %q, %v", name, err)
	}
}

// bots.getBotInfo 忽略 lang_code：任何语言都读到默认语言的值。只改 description
// 时 name/about 保持不变。
func TestBotAPIGetBotInfoReturnsDefaultForAnyLanguage(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	if _, err := f.router.BotAPISetBotInfo(f.ctx, f.bot.ID, "", domain.BotInfoUpdate{SetAbout: true, About: "short"}); err != nil {
		t.Fatalf("set about: %v", err)
	}
	if _, err := f.router.BotAPISetBotInfo(f.ctx, f.bot.ID, "", domain.BotInfoUpdate{SetDescription: true, Description: "long text"}); err != nil {
		t.Fatalf("set description: %v", err)
	}
	name, about, description, err := f.router.BotAPIGetBotInfo(f.ctx, f.bot.ID, "de")
	if err != nil || name != "BridgeBot" || about != "short" || description != "long text" {
		t.Fatalf("get(de) = %q, %q, %q, %v", name, about, description, err)
	}
}

// bots.setBotInfo 对非空 lang_code 静默返回 true 却不存储；Bot API 不能把这种
// “成功”交给机器人，必须明确报错。
func TestBotAPISetBotInfoRejectsLanguageCode(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	_, err := f.router.BotAPISetBotInfo(f.ctx, f.bot.ID, "ru", domain.BotInfoUpdate{SetAbout: true, About: "привет"})
	if err == nil || err.Error() != "LANG_CODE_NOT_SUPPORTED" {
		t.Fatalf("set with language_code err = %v, want LANG_CODE_NOT_SUPPORTED", err)
	}
	_, about, _, err := f.router.BotAPIGetBotInfo(f.ctx, f.bot.ID, "")
	if err != nil || about != "" {
		t.Fatalf("default about = %q, %v; want unchanged", about, err)
	}
}
