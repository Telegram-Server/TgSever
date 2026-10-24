package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// 编辑后的链接预览：官方服务端在编辑文本时按新文本重算预览——链接删掉了预览随之消失，
// 换了链接就换卡。发送路径（webPageMediaFromText）只在发送时生成一次，这里补齐编辑路径。

type editWebPageAction int

const (
	editWebPageKeep    editWebPageAction = iota // 不碰消息媒体
	editWebPageClear                            // 去掉链接预览
	editWebPageReplace                          // 换成 URL 指向的预览
)

// editWebPageInput 是决定编辑后预览所需的全部输入。
type editWebPageInput struct {
	// Original 是编辑前的消息媒体。
	Original *domain.MessageMedia
	// TextEdited 表示本次请求带了正文（或富文本）；只改按钮等不算。
	TextEdited bool
	Message    string
	Entities   []tg.MessageEntityClass
	NoWebpage  bool
	// WebPage 是请求里的 InputMediaWebPage：客户端指定链接与大小图偏好。
	WebPage *tg.InputMediaWebPage
}

type editWebPageDecision struct {
	Action     editWebPageAction
	URL        string
	ForceLarge bool
	ForceSmall bool
}

// decideEditedWebPage 按官方语义决定编辑后的链接预览（纯函数）：
//   - 原消息带真正媒体（图片、文件等）：只是改说明文字，保留媒体
//   - 本次没改文本：保留
//   - no_webpage：清除
//   - InputMediaWebPage：换成指定链接
//   - 新文本没有可预览链接：清除
//   - 首个链接与当前预览相同：保留，不重新抓取
//   - 否则换成新链接
func decideEditedWebPage(in editWebPageInput) editWebPageDecision {
	if !in.Original.IsZero() && in.Original.Kind != domain.MessageMediaKindWebPage {
		return editWebPageDecision{Action: editWebPageKeep}
	}
	if !in.TextEdited {
		return editWebPageDecision{Action: editWebPageKeep}
	}
	if in.NoWebpage {
		return editWebPageDecision{Action: editWebPageClear}
	}
	if in.WebPage != nil {
		return editWebPageDecision{
			Action:     editWebPageReplace,
			URL:        in.WebPage.URL,
			ForceLarge: in.WebPage.ForceLargeMedia,
			ForceSmall: in.WebPage.ForceSmallMedia,
		}
	}
	url, ok := firstPreviewableURL(in.Message, in.Entities)
	if !ok {
		return editWebPageDecision{Action: editWebPageClear}
	}
	if previewShowsURL(in.Original, url) {
		return editWebPageDecision{Action: editWebPageKeep}
	}
	return editWebPageDecision{Action: editWebPageReplace, URL: url}
}

// previewShowsURL 判断现有预览是否就是该（已规范化）链接的预览。done 卡片沿用 pending
// 占位的 id（规范化 URL 的哈希），故按 URL 或 id 任一匹配即可。
func previewShowsURL(media *domain.MessageMedia, normalized string) bool {
	if media.IsZero() || media.Kind != domain.MessageMediaKindWebPage || media.WebPage == nil {
		return false
	}
	return media.WebPage.URL == normalized || media.WebPage.ID == domain.WebPageURLHash(normalized)
}

// editedWebPageMedia 把决策落成编辑请求的 SetWebPage/Media：set=false 表示不碰媒体；
// set=true 时 media 为新预览，nil 表示清除。换链接时与发送一致：有缓存直接带卡，
// 否则挂 pending 占位，编辑成功后由 maybeEnqueueWebPageResolve 异步回写。
func (r *Router) editedWebPageMedia(ctx context.Context, original *domain.MessageMedia, d editWebPageDecision, invertMedia bool) (set bool, media *domain.MessageMedia) {
	switch d.Action {
	case editWebPageClear:
		return !original.IsZero(), nil
	case editWebPageReplace:
		media = r.webPagePendingOrCachedMedia(ctx, d.URL, invertMedia, d.ForceLarge, d.ForceSmall)
		if media == nil && original.IsZero() {
			return false, nil
		}
		return true, media
	default:
		return false, nil
	}
}

// editTargetMedia 读出待编辑消息当前的媒体。读不到（消息不存在、无权等）返回 ok=false，
// 调用方不碰预览，真正的错误交给随后的编辑落库返回。
func (r *Router) editTargetMedia(ctx context.Context, userID int64, peer domain.Peer, id int) (*domain.MessageMedia, bool) {
	switch peer.Type {
	case domain.PeerTypeUser:
		if r.deps.Messages == nil {
			return nil, false
		}
		msg, found, err := r.lookupOwnerMessage(ctx, userID, id)
		if err != nil || !found || msg.Peer != peer {
			return nil, false
		}
		return msg.Media, true
	case domain.PeerTypeChannel:
		if r.deps.Channels == nil {
			return nil, false
		}
		history, err := r.deps.Channels.GetMessages(ctx, userID, peer.ID, []int{id})
		if err != nil {
			return nil, false
		}
		for _, msg := range history.Messages {
			if msg.ID == id && !msg.Deleted {
				return msg.Media, true
			}
		}
	}
	return nil, false
}

// inputMediaWebPage 取出请求里的 InputMediaWebPage（没有则 nil）。
func inputMediaWebPage(media tg.InputMediaClass, ok bool) *tg.InputMediaWebPage {
	if !ok {
		return nil
	}
	page, _ := media.(*tg.InputMediaWebPage)
	return page
}
