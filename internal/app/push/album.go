package push

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// 相册（同一 grouped_id 的一组消息，sendMultiMedia）合并成一条推送：
//
// 带 GroupedID 的消息先放进待合并缓冲区，私聊按 (会话, 收件人, grouped_id)、群组/频道按
// (会话, grouped_id) 归组（在成员扇出之前）。每来一项重新计时 Config.AlbumDebounce，
// 但从第一项起最多等 Config.AlbumMaxWait；到时只推一次：msg_id 是最后一项（最大的 msg_id），
// 文案按整组的类型选 *_MESSAGE_PHOTOS / *_MESSAGE_VIDEOS / *_MESSAGE_DOCS，混合时 *_MESSAGES。
// 只有一项的组照原样推送那一项。合并后的推送与单条消息走同一条路径（通知规则、前台、
// other_uids、关闭预览的 *_NOTEXT 都一样）。关停时缓冲区里的组立即放进队列，在关停期限内发出。

const (
	defaultAlbumDebounce = time.Second
	defaultAlbumMaxWait  = 3 * time.Second
)

// 相册类型：整组都是同一类型时用对应的复数键，否则 albumMixed。
const (
	albumPhotos = "PHOTOS"
	albumVideos = "VIDEOS"
	albumDocs   = "DOCS"
	albumMixed  = ""
)

// albumSummary 是合并后的相册：项数与整组的类型（albumPhotos…，混合时 albumMixed）。
type albumSummary struct {
	count int
	kind  string
}

// albumKey 标识一组待合并的相册。channel 区分群组/频道扇出与私聊路径；私聊的 recipient 是唯一收件人。
type albumKey struct {
	channel   bool
	chat      Chat
	recipient int64
	groupedID int64
}

type pendingAlbum struct {
	last  NewMessage
	count int
	kind  string
	first time.Time
	timer *time.Timer
}

// albumBuffer 是待合并的相册；flush 把合并好的消息放进推送队列。
type albumBuffer struct {
	mu      sync.Mutex
	pending map[albumKey]*pendingAlbum
}

func newAlbumKey(channel bool, msg NewMessage) albumKey {
	key := albumKey{channel: channel, chat: msg.Chat, groupedID: msg.GroupedID}
	key.chat.TopicID = 0
	if !channel && len(msg.Recipients) == 1 {
		key.recipient = msg.Recipients[0]
	}
	return key
}

// bufferAlbumItem 把相册的一项放进缓冲区；返回 false 表示服务正在关停，调用方直接推送这一项。
func (s *Service) bufferAlbumItem(channel bool, msg NewMessage) bool {
	key := newAlbumKey(channel, msg)
	kind := albumItemKind(msg)
	now := time.Now()
	b := &s.albums
	b.mu.Lock()
	// 在锁内看 stopping：关停时 flushAlbums 取走缓冲区之后到达的项不再缓冲（否则没人发出）。
	if s.stopping.Load() {
		b.mu.Unlock()
		return false
	}
	p, ok := b.pending[key]
	if !ok {
		if b.pending == nil {
			b.pending = make(map[albumKey]*pendingAlbum)
		}
		p = &pendingAlbum{last: msg, count: 1, kind: kind, first: now}
		b.pending[key] = p
		p.timer = time.AfterFunc(min(s.cfg.AlbumDebounce, s.cfg.AlbumMaxWait), func() { s.flushAlbum(key, p) })
		b.mu.Unlock()
		return true
	}
	p.count++
	if p.kind != kind {
		p.kind = albumMixed
	}
	if msg.MessageID >= p.last.MessageID {
		p.last = msg
	}
	wait := min(s.cfg.AlbumDebounce, s.cfg.AlbumMaxWait-now.Sub(p.first))
	if wait > 0 {
		p.timer.Reset(wait)
		b.mu.Unlock()
		return true
	}
	delete(b.pending, key)
	p.timer.Stop()
	b.mu.Unlock()
	s.enqueueAlbum(key, p)
	return true
}

// flushAlbum 在计时到期时发出这一组；组已被发出（或被同 key 的新组替换）时什么也不做。
func (s *Service) flushAlbum(key albumKey, p *pendingAlbum) {
	b := &s.albums
	b.mu.Lock()
	if b.pending[key] != p {
		b.mu.Unlock()
		return
	}
	delete(b.pending, key)
	b.mu.Unlock()
	s.enqueueAlbum(key, p)
}

// flushAlbums 在关停时把缓冲区里所有组立即放进队列。
func (s *Service) flushAlbums() {
	b := &s.albums
	b.mu.Lock()
	pending := b.pending
	b.pending = nil
	b.mu.Unlock()
	for key, p := range pending {
		p.timer.Stop()
		s.enqueueAlbum(key, p)
	}
}

// enqueueAlbum 把合并好的相册放进对应路径的队列（只有一项时原样推送那一项）。
func (s *Service) enqueueAlbum(key albumKey, p *pendingAlbum) {
	msg := p.last
	if p.count > 1 {
		msg.album = &albumSummary{count: p.count, kind: p.kind}
	}
	if key.channel {
		s.enqueueChannelMessage(msg)
		return
	}
	s.enqueueNewMessage(msg)
}

// albumItemKind 是相册一项的类型：图片、视频、文件（含音乐），其余按混合处理。
func albumItemKind(msg NewMessage) string {
	if msg.Media.IsZero() {
		return albumMixed
	}
	suffix, _, ok := classifyMedia(msg.Media)
	if !ok {
		return albumMixed
	}
	switch suffix {
	case suffixPhoto:
		return albumPhotos
	case suffixVideo:
		return albumVideos
	case suffixDoc:
		return albumDocs
	default:
		return albumMixed
	}
}

// albumAlert 选出合并相册的文案键、参数与选复数形式用的数量（顺序以 iOS 语言包为准）：
// 会话参数（同 messageAlert）之后是项数。混合相册的 *_MESSAGES 文案是“Album”，按一个相册选形式。
func albumAlert(msg NewMessage, senderName, chatTitle string) (Key, []string, int) {
	prefix := keyPrefix(msg.Chat.Kind)
	count := msg.album.count
	args := append(chatArgs(msg.Chat.Kind, senderName, chatTitle), strconv.Itoa(count))
	if msg.album.kind == albumMixed {
		return Key(strings.TrimSuffix(prefix, "_") + "S"), args, 1
	}
	return Key(prefix + msg.album.kind), args, count
}
