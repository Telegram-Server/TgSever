package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

// MTProto 的 HTML / Markdown 富文本由服务端解析。非法输入必须在转换层被拒绝，
// 而不是靠网关的 Bot API 投影顺带拦下。
func TestRichMessageSourceRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name  string
		input tg.InputRichMessageClass
		want  string
	}{
		{"empty HTML", &tg.InputRichMessageHTML{HTML: ""}, "RICH_MESSAGE_INVALID"},
		{"HTML with files", &tg.InputRichMessageHTML{HTML: "<p>x</p>", Files: []tg.InputRichFileClass{&tg.InputRichFilePhoto{}}}, "RICH_MESSAGE_INVALID"},
		{"HTML that parses to nothing", &tg.InputRichMessageHTML{HTML: "<p></p>"}, "RICH_MESSAGE_INVALID"},
		{"HTML of whitespace", &tg.InputRichMessageHTML{HTML: "   "}, "RICH_MESSAGE_INVALID"},
		{"HTML with a bad date", &tg.InputRichMessageHTML{HTML: `<p><tg-time unix="-1" format="R">x</tg-time></p>`}, "RICH_MESSAGE_DATE_INVALID"},
		{"HTML with remote media", &tg.InputRichMessageHTML{HTML: `<p>x</p><img src="https://example.test/a.png">`}, "WEBPAGE_MEDIA_EMPTY"},
		{"empty Markdown", &tg.InputRichMessageMarkdown{Markdown: ""}, "RICH_MESSAGE_INVALID"},
		{"Markdown of whitespace", &tg.InputRichMessageMarkdown{Markdown: "  \n\n "}, "RICH_MESSAGE_INVALID"},
		{"Markdown with files", &tg.InputRichMessageMarkdown{Markdown: "x", Files: []tg.InputRichFileClass{&tg.InputRichFilePhoto{}}}, "RICH_MESSAGE_INVALID"},
	}
	r := &Router{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rich, err := r.domainRichMessageFromInput(context.Background(), tc.input)
			if !tgerr.Is(err, tc.want) {
				t.Fatalf("rich = %+v err = %v, want %s", rich, err, tc.want)
			}
		})
	}
}

// 网关的 sendRichMessage 仍然走同一套解析器：Bot API 的 HTML 输入被转换成 MTProto
// 的 InputRichMessageHTML，再由转换层解析。
func TestBotAPIRichInputReusesMTProtoParser(t *testing.T) {
	r := &Router{}
	input, err := tgInputRichMessageFromBotAPI(domain.BotAPIRichMessageInput{HTML: "<h2>Меню</h2><p><b>активна</b></p>"})
	if err != nil {
		t.Fatalf("Bot API input: %v", err)
	}
	rich, err := r.domainRichMessageFromInput(context.Background(), input)
	if err != nil {
		t.Fatalf("parse Bot API HTML: %v", err)
	}
	decoded, err := tgRichMessage(rich)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Blocks) != 2 {
		t.Fatalf("blocks = %#v, want heading and paragraph", decoded.Blocks)
	}
	if _, ok := decoded.Blocks[0].(*tg.PageBlockHeading2); !ok {
		t.Fatalf("first block = %T, want heading2", decoded.Blocks[0])
	}
	if len(rich.BotAPIProjection) == 0 {
		t.Fatal("gateway lost its Bot API projection")
	}
}
