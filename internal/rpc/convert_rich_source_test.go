package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

// MTProto 的 HTML / Markdown 富文本由服务端解析。非法输入必须在转换层被拒绝。
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
