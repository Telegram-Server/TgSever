package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

type suggestLangPackStub struct {
	LangPackService
	languages map[string][]string
	packs     []string
}

func (s *suggestLangPackStub) ListLanguages(_ context.Context, langPack string) ([]domain.LangPackLanguage, error) {
	s.packs = append(s.packs, langPack)
	codes, ok := s.languages[langPack]
	if !ok {
		return nil, domain.ErrLangPackInvalid
	}
	out := make([]domain.LangPackLanguage, 0, len(codes))
	for _, code := range codes {
		out = append(out, domain.LangPackLanguage{LangPack: langPack, LangCode: code})
	}
	return out, nil
}

// 映射规则的完整矩阵在 domain.TestSuggestLangCode；这里只测接线：help.getConfig
// 用 initConnection 的 system_lang_code 与 lang_pack 选出推荐语言，未登录也下发。
func TestHelpGetConfigSuggestsLangFromSystemLangCode(t *testing.T) {
	packs := &suggestLangPackStub{languages: map[string][]string{
		"android": {"en", "zh-hans", "zh-hant"},
	}}
	r := New(Config{DC: 2, PublicBaseURL: "https://telesrv.net"}, Deps{LangPack: packs}, zaptest.NewLogger(t), clock.System)

	ctx := WithClientInfo(context.Background(), ClientInfo{SystemLangCode: "zh-cn", LangPack: "android", LangCode: "en"})
	config, err := r.onHelpGetConfig(ctx)
	if err != nil {
		t.Fatalf("help.getConfig: %v", err)
	}
	if got, ok := config.GetSuggestedLangCode(); !ok || got != "zh-hans" {
		t.Fatalf("suggested_lang_code = %q (set %v), want zh-hans", got, ok)
	}
	if len(packs.packs) != 1 || packs.packs[0] != "android" {
		t.Fatalf("ListLanguages packs = %v, want [android]", packs.packs)
	}

	// 没有 initConnection 信息、或该平台语言包查询失败：不下发，getConfig 照常成功。
	for name, ctx := range map[string]context.Context{
		"no client info": context.Background(),
		"unknown pack":   WithClientInfo(context.Background(), ClientInfo{SystemLangCode: "zh-cn", LangPack: "nope"}),
	} {
		config, err := r.onHelpGetConfig(ctx)
		if err != nil {
			t.Fatalf("%s: help.getConfig: %v", name, err)
		}
		if got, ok := config.GetSuggestedLangCode(); ok {
			t.Fatalf("%s: suggested_lang_code = %q, want unset", name, got)
		}
	}
}
