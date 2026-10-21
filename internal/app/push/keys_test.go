package push

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"telesrv/internal/domain"
)

func photoMedia() *domain.MessageMedia {
	return &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 1}}
}

func documentMedia(mime string, attrs ...domain.DocumentAttribute) *domain.MessageMedia {
	return &domain.MessageMedia{Kind: domain.MessageMediaKindDocument, Document: &domain.Document{ID: 2, MimeType: mime, Attributes: attrs}}
}

func videoMedia() *domain.MessageMedia {
	m := documentMedia("video/mp4", domain.DocumentAttribute{Kind: domain.DocAttrVideo, W: 640, H: 480, Duration: 3})
	m.Video = true
	return m
}

func gifMedia() *domain.MessageMedia {
	return documentMedia("video/mp4",
		domain.DocumentAttribute{Kind: domain.DocAttrAnimated},
		domain.DocumentAttribute{Kind: domain.DocAttrVideo, W: 320, H: 240, Duration: 2})
}

func roundMedia() *domain.MessageMedia {
	m := documentMedia("video/mp4", domain.DocumentAttribute{Kind: domain.DocAttrVideo, W: 240, H: 240, Duration: 4, RoundMessage: true})
	m.Round = true
	return m
}

func voiceMedia() *domain.MessageMedia {
	m := documentMedia("audio/ogg", domain.DocumentAttribute{Kind: domain.DocAttrAudio, Voice: true, AudioDuration: 3})
	m.Voice = true
	return m
}

func stickerMedia(alt string) *domain.MessageMedia {
	return documentMedia("image/webp", domain.DocumentAttribute{Kind: domain.DocAttrSticker, Alt: alt})
}

func pollMedia(question string, quiz bool) *domain.MessageMedia {
	return &domain.MessageMedia{Kind: domain.MessageMediaKindPoll, Poll: &domain.MessagePoll{ID: 3, Question: question, Quiz: quiz}}
}

// mediaCase 是一种消息类型在三种会话里的期望：suffix 是去掉 MESSAGE_/CHAT_MESSAGE_/CHANNEL_MESSAGE_ 前缀的键，
// extra 是会话参数之后的类型参数（贴纸 emoji、联系人名、投票问题……）。
type mediaCase struct {
	name   string
	text   string
	media  *domain.MessageMedia
	suffix string
	extra  []string
}

func mediaCases() []mediaCase {
	return []mediaCase{
		{name: "text", text: "hello", suffix: "TEXT", extra: []string{"hello"}},
		{name: "web page preview is text", text: "see https://x.test", media: &domain.MessageMedia{Kind: domain.MessageMediaKindWebPage}, suffix: "TEXT", extra: []string{"see https://x.test"}},
		{name: "photo", media: photoMedia(), suffix: "PHOTO"},
		{name: "video", media: videoMedia(), suffix: "VIDEO"},
		{name: "video by attribute only", media: documentMedia("video/mp4", domain.DocumentAttribute{Kind: domain.DocAttrVideo, W: 1, H: 1, Duration: 1}), suffix: "VIDEO"},
		{name: "file", media: documentMedia("application/pdf", domain.DocumentAttribute{Kind: domain.DocAttrFilename, FileName: "a.pdf"}), suffix: "DOC"},
		{name: "music file", media: documentMedia("audio/mpeg", domain.DocumentAttribute{Kind: domain.DocAttrAudio, Title: "song"}), suffix: "DOC"},
		{name: "sticker", media: stickerMedia("😀"), suffix: "STICKER", extra: []string{"😀"}},
		{name: "sticker without emoji", media: stickerMedia(""), suffix: "STICKER", extra: []string{""}},
		{name: "voice", media: voiceMedia(), suffix: "AUDIO"},
		{name: "voice by attribute only", media: documentMedia("audio/ogg", domain.DocumentAttribute{Kind: domain.DocAttrAudio, Voice: true}), suffix: "AUDIO"},
		{name: "round video", media: roundMedia(), suffix: "ROUND"},
		{name: "gif", media: gifMedia(), suffix: "GIF"},
		{name: "contact", media: &domain.MessageMedia{Kind: domain.MessageMediaKindContact, Contact: &domain.MessageContact{FirstName: "Bob", LastName: "Lee", PhoneNumber: "+100"}}, suffix: "CONTACT", extra: []string{"Bob Lee"}},
		{name: "contact without name", media: &domain.MessageMedia{Kind: domain.MessageMediaKindContact, Contact: &domain.MessageContact{PhoneNumber: "+100"}}, suffix: "CONTACT", extra: []string{"+100"}},
		{name: "location", media: &domain.MessageMedia{Kind: domain.MessageMediaKindGeo, Geo: &domain.MessageGeoPoint{}}, suffix: "GEO"},
		{name: "venue", media: &domain.MessageMedia{Kind: domain.MessageMediaKindVenue}, suffix: "GEO"},
		{name: "live location", media: &domain.MessageMedia{Kind: domain.MessageMediaKindGeoLive}, suffix: "GEOLIVE"},
		{name: "poll", media: pollMedia("Lunch?", false), suffix: "POLL", extra: []string{"Lunch?"}},
		{name: "quiz", media: pollMedia("2+2?", true), suffix: "QUIZ", extra: []string{"2+2?"}},
		// 带文字说明的媒体：iOS 客户端（及官方服务端）的约定是走 *_TEXT，正文是“类型 emoji + 说明”。
		{name: "photo with caption", text: "sunset", media: photoMedia(), suffix: "TEXT", extra: []string{"📷 sunset"}},
		{name: "video with caption", text: "clip", media: videoMedia(), suffix: "TEXT", extra: []string{"📹 clip"}},
		{name: "gif with caption", text: "lol", media: gifMedia(), suffix: "TEXT", extra: []string{"📹 lol"}},
		{name: "file with caption", text: "report", media: documentMedia("application/pdf"), suffix: "TEXT", extra: []string{"📎 report"}},
		{name: "voice with caption", text: "listen", media: voiceMedia(), suffix: "TEXT", extra: []string{"🎤 listen"}},
		// 没覆盖的类型一律 *_NOTEXT。
		{name: "dice", media: &domain.MessageMedia{Kind: domain.MessageMediaKindDice}, suffix: "NOTEXT"},
		{name: "todo", media: &domain.MessageMedia{Kind: domain.MessageMediaKindTodo}, suffix: "NOTEXT"},
		{name: "story", media: &domain.MessageMedia{Kind: domain.MessageMediaKindStory}, suffix: "NOTEXT"},
		{name: "invoice", media: &domain.MessageMedia{Kind: domain.MessageMediaKindInvoice}, suffix: "NOTEXT"},
		{name: "giveaway", media: &domain.MessageMedia{Kind: domain.MessageMediaKindGiveaway}, suffix: "NOTEXT"},
		{name: "unknown kind with caption", text: "x", media: &domain.MessageMedia{Kind: "future"}, suffix: "NOTEXT"},
		{name: "photo without payload", media: &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto}, suffix: "NOTEXT"},
		{name: "empty message", suffix: "NOTEXT"},
	}
}

func TestMessageAlertKeyAndArgsPerTypeAndChatKind(t *testing.T) {
	chats := []struct {
		name   string
		kind   ChatKind
		prefix string
		base   []string
	}{
		// 私聊：%1$@ 发送者。
		{name: "private", kind: ChatPrivate, prefix: "MESSAGE_", base: []string{"Alice"}},
		// 群组：%1$@ 发送者，%2$@ 群名。
		{name: "group", kind: ChatGroup, prefix: "CHAT_MESSAGE_", base: []string{"Alice", "Team"}},
		// 频道：%1$@ 频道名（不显示发送者）。
		{name: "channel", kind: ChatChannel, prefix: "CHANNEL_MESSAGE_", base: []string{"News"}},
	}
	for _, chat := range chats {
		for _, tc := range mediaCases() {
			t.Run(chat.name+"/"+tc.name, func(t *testing.T) {
				title := map[ChatKind]string{ChatGroup: "Team", ChatChannel: "News"}[chat.kind]
				msg := NewMessage{Chat: Chat{Kind: chat.kind, ID: 9}, Text: tc.text, Media: tc.media}
				key, args := messageAlert(msg, "Alice", title, true)
				wantKey := Key(chat.prefix + tc.suffix)
				wantArgs := append(append([]string(nil), chat.base...), tc.extra...)
				if key != wantKey || !reflect.DeepEqual(args, wantArgs) {
					t.Fatalf("messageAlert = %s %q, want %s %q", key, args, wantKey, wantArgs)
				}
			})
		}
	}
}

func TestMessageAlertWithoutPreviewIsNoTextWithoutContent(t *testing.T) {
	for _, chat := range []struct {
		kind ChatKind
		key  Key
		args []string
	}{
		{ChatPrivate, "MESSAGE_NOTEXT", []string{"Alice"}},
		{ChatGroup, "CHAT_MESSAGE_NOTEXT", []string{"Alice", "Team"}},
		{ChatChannel, "CHANNEL_MESSAGE_NOTEXT", []string{"News"}},
	} {
		for _, tc := range mediaCases() {
			title := map[ChatKind]string{ChatGroup: "Team", ChatChannel: "News"}[chat.kind]
			msg := NewMessage{Chat: Chat{Kind: chat.kind, ID: 9}, Text: tc.text, Media: tc.media}
			key, args := messageAlert(msg, "Alice", title, false)
			if key != chat.key || !reflect.DeepEqual(args, chat.args) {
				t.Fatalf("%v/%s without preview = %s %q, want %s %q", chat.kind, tc.name, key, args, chat.key, chat.args)
			}
		}
	}
}

// 每个可能产出的键都有内置英文兜底，且与 iOS en 语言包逐字一致（参数顺序以客户端为准）。
func TestBuiltinTemplatesMatchIOSEnglishLangPack(t *testing.T) {
	en := loadIOSEnglishPushStrings(t)
	for _, prefix := range []string{"MESSAGE_", "CHAT_MESSAGE_", "CHANNEL_MESSAGE_"} {
		for _, tc := range mediaCases() {
			key := Key(prefix + tc.suffix)
			builtin, ok := builtinTemplates[key]
			if !ok {
				t.Fatalf("no builtin template for %s", key)
			}
			if want, ok := en["PUSH_"+string(key)]; !ok || want != builtin {
				t.Fatalf("builtin %s = %q, iOS en langpack has %q (present=%v)", key, builtin, want, ok)
			}
		}
	}
}

// 用 iOS en 语言包渲染几种类型，确认参数位置与客户端文案对得上。
func TestRenderMediaAlertsWithIOSEnglishLangPack(t *testing.T) {
	r := NewRenderer(fakeLangPack{"en": loadIOSEnglishPushStrings(t)})
	cases := []struct {
		chat        Chat
		title       string
		media       *domain.MessageMedia
		text        string
		wantTitle   string
		wantBodyEnd string
	}{
		{Chat{Kind: ChatPrivate}, "", photoMedia(), "", "Alice", "📷 Photo"},
		{Chat{Kind: ChatPrivate}, "", stickerMedia("😀"), "", "Alice", "😀 Sticker"},
		{Chat{Kind: ChatPrivate}, "", photoMedia(), "sunset", "Alice", "📷 sunset"},
		{Chat{Kind: ChatGroup}, "Team", pollMedia("Lunch?", false), "", "Team", "Alice: 📊 Poll \"Lunch?\""},
		{Chat{Kind: ChatGroup}, "Team", &domain.MessageMedia{Kind: domain.MessageMediaKindContact, Contact: &domain.MessageContact{FirstName: "Bob"}}, "", "Team", "Alice: 👤 Bob"},
		{Chat{Kind: ChatGroup}, "Team", &domain.MessageMedia{Kind: domain.MessageMediaKindDice}, "", "Team", "Alice: New message"},
		{Chat{Kind: ChatChannel}, "News", voiceMedia(), "", "News", "🎤 Voice message"},
		{Chat{Kind: ChatChannel}, "News", videoMedia(), "clip", "News", "📹 clip"},
	}
	for _, tc := range cases {
		key, args := messageAlert(NewMessage{Chat: tc.chat, Text: tc.text, Media: tc.media}, "Alice", tc.title, true)
		got := r.Render(context.Background(), "en", key, args...)
		if got.Title != tc.wantTitle || got.Body != tc.wantBodyEnd {
			t.Fatalf("%s rendered %+v, want title %q body %q", key, got, tc.wantTitle, tc.wantBodyEnd)
		}
	}
}

// loadIOSEnglishPushStrings 读取仓库里的 iOS en 语言包中所有 PUSH_* 字符串。
func loadIOSEnglishPushStrings(t *testing.T) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", "..", "data", "langpack", "ios", "ios_en_v*.strings"))
	if err != nil || len(paths) == 0 {
		t.Skipf("iOS en langpack not found (err=%v)", err)
	}
	f, err := os.Open(paths[len(paths)-1])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]string{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, `"PUSH_`) || !strings.HasSuffix(line, ";") {
			continue
		}
		name, value, ok := strings.Cut(strings.TrimSuffix(line, ";"), " = ")
		if !ok {
			continue
		}
		k, err1 := strconv.Unquote(name)
		v, err2 := strconv.Unquote(value)
		if err1 != nil || err2 != nil {
			continue
		}
		out[k] = v
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// 服务层：私聊图片推送的解密内容是渲染好的“📷 Photo”，不是空白或通用文案。
func TestNewMessagePushRendersMediaDescription(t *testing.T) {
	f := newServiceFixture(t, Config{})
	secret := testSecret()
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", secret, presenceNone)
	f.start()
	msg := textFromAlice("")
	msg.Media = photoMedia()
	f.svc.NewMessage(msg)
	f.stop()

	if len(f.sender.sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(f.sender.sent))
	}
	var outer struct {
		P string `json:"p"`
	}
	if err := json.Unmarshal(f.sender.sent[0].Payload, &outer); err != nil {
		t.Fatal(err)
	}
	plain, _, err := nseDecrypt(secret, outer.P)
	if err != nil {
		t.Fatal(err)
	}
	alert := decodeJSONObject(t, plain)["aps"].(map[string]any)["alert"].(map[string]any)
	if alert["title"] != "Alice A" || alert["body"] != "📷 Photo" {
		t.Fatalf("decrypted payload = %s, want Alice A / 📷 Photo", plain)
	}
}
