package rpc

import (
	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// tgInputRichMessageFromBotAPI 把 Bot API 的富文本输入转成 MTProto 构造器，之后与
// MTProto 客户端发来的 HTML / Markdown 走同一个解析器（convert_rich_source.go）。
func tgInputRichMessageFromBotAPI(input domain.BotAPIRichMessageInput) (tg.InputRichMessageClass, error) {
	if input.SourceCount() != 1 || len(input.BlocksJSON) != 0 {
		return nil, richMessageInvalidErr()
	}
	if len(input.MediaJSON) != 0 {
		return nil, richMessageMediaUnsupportedErr()
	}
	if input.HTML != "" {
		return &tg.InputRichMessageHTML{
			Rtl: input.RTL, Noautolink: input.SkipEntityDetection, HTML: input.HTML,
		}, nil
	}
	if input.Markdown != "" {
		return &tg.InputRichMessageMarkdown{
			Rtl: input.RTL, Noautolink: input.SkipEntityDetection, Markdown: input.Markdown,
		}, nil
	}
	return nil, richMessageInvalidErr()
}
