package mtprotoedge

import (
	"context"
	"net"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/crypto"
	"github.com/iamxvbaba/td/exchange"
	"github.com/iamxvbaba/td/mt"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/transport"

	"telesrv/internal/app/auth"
	botsapp "telesrv/internal/app/bots"
	channelsapp "telesrv/internal/app/channels"
	"telesrv/internal/app/dialogs"
	"telesrv/internal/app/help"
	messageapp "telesrv/internal/app/messages"
	"telesrv/internal/app/updates"
	"telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/rpc"
	"telesrv/internal/store/memory"
)

// 本文件是 TDLib 作为 bot 接入本服务的契约测试（#143）。
//
// telegram-bot-api 以 bot 身份经 TDLib 连 MTProto。测试不运行 TDLib，而是在真实 TCP
// 监听上用原始 MTProto 帧逐条模仿 TDLib 的请求方式（Layer、包装、参数），并断言 TDLib
// 会检查的字段。每条断言注明 TDLib 源码位置，路径相对 tdlib/telegram-bot-api 10.3
// （TDLib 在其 td 子模块）；TDLib 升级时按这些位置逐条核对。

// tdlibMTProtoLayer 是 TDLib 发送的 Layer：td/td/telegram/Version.h:13 MTPROTO_LAYER。
const tdlibMTProtoLayer = 229

// tdlibBotConn 是模仿 TDLib 的原始 MTProto 连接：首个请求带
// invokeWithLayer+initConnection 头，之后的请求不再包装
// （td/td/telegram/net/MtprotoHeader.cpp:31-34，头只随会话首个请求发送）。
type tdlibBotConn struct {
	t      *testing.T
	conn   transport.Conn
	auth   exchange.ClientExchangeResult
	cipher crypto.Cipher
	ids    *proto.MessageIDGen
	seq    int32
}

func dialTDLibBot(t *testing.T, addr string, dc int, pub exchange.PublicKey) *tdlibBotConn {
	t.Helper()
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)
	return &tdlibBotConn{t: t, conn: conn, auth: auth, cipher: cipher, ids: proto.NewMessageIDGen(time.Now)}
}

// tdlibInitConnection 按 telegram-bot-api 的 setTdlibParameters（telegram-bot-api/Client.cpp:9411-9422：
// device_model="server"、system_language_code="en"）与 MtprotoHeader.cpp 的 initConnection
// 编码（flags.1 带 params）包装 query。
func tdlibInitConnection(query bin.Object) *tg.InvokeWithLayerRequest {
	init := &tg.InitConnectionRequest{
		APIID:          94575, // 任意 api_id：bot 登录不校验应用凭据。
		DeviceModel:    "server",
		SystemVersion:  "Linux",
		AppVersion:     "10.3",
		SystemLangCode: "en",
		LangPack:       "",
		LangCode:       "en",
		Query:          query,
	}
	init.SetParams(&tg.JSONObject{Value: []tg.JSONObjectValue{{Key: "tz_offset", Value: &tg.JSONNumber{Value: 0}}}})
	return &tg.InvokeWithLayerRequest{Layer: tdlibMTProtoLayer, Query: init}
}

// call 发送一个内容消息并等待对应 rpc_result；返回结果 buffer 或 RPC 错误。
func (c *tdlibBotConn) call(req bin.Encoder) (*bin.Buffer, *mt.RPCError) {
	c.t.Helper()
	msgID := c.ids.New(proto.MessageFromClient)
	seq := c.seq*2 + 1
	c.seq++
	sendEncryptedWithSeq(c.t, c.conn, c.cipher, c.auth, msgID, seq, req)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for i := 0; i < 64; i++ {
		_, _, plain := readServerMessageContext(c.t, ctx, c.conn, c.cipher, c.auth.AuthKey)
		if out, rpcErr, ok := c.matchResult(plain, msgID); ok {
			return out, rpcErr
		}
	}
	c.t.Fatalf("no rpc_result for msg %d", msgID)
	return nil, nil
}

func (c *tdlibBotConn) matchResult(plain *bin.Buffer, msgID int64) (*bin.Buffer, *mt.RPCError, bool) {
	c.t.Helper()
	id, err := plain.PeekID()
	if err != nil {
		c.t.Fatalf("peek server message: %v", err)
	}
	switch id {
	case proto.MessageContainerTypeID:
		var container proto.MessageContainer
		if err := container.Decode(plain); err != nil {
			c.t.Fatalf("decode container: %v", err)
		}
		for _, msg := range container.Messages {
			if out, rpcErr, ok := c.matchResult(&bin.Buffer{Buf: msg.Body}, msgID); ok {
				return out, rpcErr, true
			}
		}
		return nil, nil, false
	case proto.ResultTypeID:
		var result proto.Result
		if err := result.Decode(plain); err != nil {
			c.t.Fatalf("decode rpc_result: %v", err)
		}
		if result.RequestMessageID != msgID {
			return nil, nil, false
		}
		inner := &bin.Buffer{Buf: result.Result}
		if innerID, _ := inner.PeekID(); innerID == proto.GZIPTypeID {
			var packed proto.GZIP
			if err := packed.Decode(inner); err != nil {
				c.t.Fatalf("decode gzip_packed: %v", err)
			}
			inner = &bin.Buffer{Buf: packed.Data}
		}
		if innerID, _ := inner.PeekID(); innerID == mt.RPCErrorTypeID {
			var rpcErr mt.RPCError
			if err := rpcErr.Decode(inner); err != nil {
				c.t.Fatalf("decode rpc_error: %v", err)
			}
			return nil, &rpcErr, true
		}
		return inner, nil, true
	default:
		return nil, nil, false
	}
}

// mustCall 断言请求成功并返回结果 buffer。
func (c *tdlibBotConn) mustCall(name string, req bin.Encoder) *bin.Buffer {
	c.t.Helper()
	out, rpcErr := c.call(req)
	if rpcErr != nil {
		c.t.Fatalf("%s: rpc error %d %s", name, rpcErr.ErrorCode, rpcErr.ErrorMessage)
	}
	return out
}

func decodeInto[T any, P interface {
	*T
	bin.Decoder
}](t *testing.T, name string, b *bin.Buffer) *T {
	t.Helper()
	var v T
	if err := P(&v).Decode(b); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return &v
}

type tdlibContractServer struct {
	addr string
	pub  exchange.PublicKey
	dc   int
	ip   string
	port int

	users    *memory.UserStore
	bots     *botsapp.Service
	channels *channelsapp.Service
	files    *tdlibContractFiles
}

func startTDLibContractServer(t *testing.T) tdlibContractServer {
	t.Helper()
	const dc = 2
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	tcpAddr := ln.Addr().(*net.TCPAddr)

	userStore := memory.NewUserStore()
	authzStore := memory.NewAuthorizationStore()
	authKeyStore := memory.NewAuthKeyStore()
	helpStore := memory.NewHelpStore()
	dialogStore := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogStore)
	botStore := memory.NewBotStore(userStore)
	botsService := botsapp.NewService(userStore, botStore, messageStore, botsapp.WithLogger(zaptest.NewLogger(t).Named("bots")))
	// bot qts 序列（#145）：成员变动在 channel store 的同一临界区里写入。
	botUpdates := memory.NewBotUpdateStore(userStore)
	channelStore := memory.NewChannelStore()
	channelStore.AttachBotUpdateStore(botUpdates)
	channelsService := channelsapp.NewService(channelStore, channelsapp.WithBotProfileResolver(botsService))
	sessions := NewSessionManager(zaptest.NewLogger(t).Named("sessions"))
	files := &tdlibContractFiles{}
	deps := rpc.Deps{
		Auth: auth.NewService(userStore, authzStore, memory.NewCodeStore(), authKeyStore,
			memory.NewTempAuthKeyBindingStore(authKeyStore), "12345", auth.WithBotLogin(botStore)),
		Help:       help.NewService(helpStore, helpStore),
		Users:      users.NewService(userStore),
		Updates:    updates.NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore()),
		Dialogs:    dialogs.NewService(dialogStore),
		Messages:   messageapp.NewService(messageStore, dialogStore, messageapp.WithBotResponder(botsService)),
		Bots:       botsService,
		Channels:   channelsService,
		BotUpdates: botUpdates,
		Files:      files,
		Sessions:   sessions,
	}
	router := rpc.New(rpc.Config{DC: dc, IP: tcpAddr.IP.String(), Port: tcpAddr.Port}, deps, zaptest.NewLogger(t), clock.System)
	botsService.SetRouterHooks(router)
	addr, pub, _ := startTestServerOnListener(t, ln, Options{DC: dc, AuthKeys: authKeyStore, LayerRPC: router, ActiveSessions: sessions})
	return tdlibContractServer{
		addr: addr, pub: pub, dc: dc, ip: tcpAddr.IP.String(), port: tcpAddr.Port,
		users: userStore, bots: botsService, channels: channelsService, files: files,
	}
}

// tdlibContractFiles 只实现 upload.getFile 用到的 GetFile，只认 doc:777001；
// 其余 FilesService 方法未实现（被调用即 panic，说明契约路径超出预期）。
type tdlibContractFiles struct {
	rpc.FilesService
}

func (f *tdlibContractFiles) GetFile(_ context.Context, req domain.FileDownloadRequest) (domain.FileChunk, bool, error) {
	if req.LocationKey != "doc:777001" {
		return domain.FileChunk{}, false, nil
	}
	return domain.FileChunk{MimeType: "application/octet-stream", Bytes: []byte("tdlib-bot-file")}, true, nil
}

func TestTDLibBotContract(t *testing.T) {
	t.Parallel()
	srv := startTDLibContractServer(t)
	bot := dialTDLibBot(t, srv.addr, srv.dc, srv.pub)

	// 首个请求：invokeWithLayer(229, initConnection{help.getConfig})，未授权发送
	// （td/td/telegram/ConfigManager.cpp:1025）。
	cfg := decodeInto[tg.Config](t, "help.getConfig", bot.mustCall("invokeWithLayer(initConnection(help.getConfig))", tdlibInitConnection(&tg.HelpGetConfigRequest{})))
	// this_dc 必须等于主 DC，否则 TDLib 视为外部配置并立即重拉（ConfigManager.cpp:1173-1318）。
	if cfg.ThisDC != srv.dc {
		t.Errorf("config.this_dc = %d, want main dc %d", cfg.ThisDC, srv.dc)
	}
	// test_mode 与客户端 is_test 不一致会报错（ConfigManager.cpp:1185）。本服务只有生产模式，
	// telegram-bot-api 以 use_test_dc=false 启动（Client.cpp:9412）。
	if cfg.TestMode {
		t.Errorf("config.test_mode = true, want false")
	}
	// tmp_sessions > 1 会让 TDLib 启用 PFS 并发 auth.bindTempAuthKey
	// （NetQueryDispatcher.cpp:368、ConfigManager.cpp:1218、Session.cpp:1383）。
	if n, ok := cfg.GetTmpSessions(); ok && n > 1 {
		t.Errorf("config.tmp_sessions = %d, want <= 1", n)
	}
	// dc_options 接管 TDLib 的默认 DC 表（ConfigManager.cpp:1189-1191），必须指回本服务。
	if len(cfg.DCOptions) == 0 {
		t.Errorf("config.dc_options empty")
	}
	for _, option := range cfg.DCOptions {
		if option.ID != srv.dc || option.IPAddress != srv.ip || option.Port != srv.port {
			t.Errorf("config.dc_options entry %+v, want dc %d at %s:%d", option, srv.dc, srv.ip, srv.port)
		}
	}
	// help.getNearestDc 用来校验主 auth key（td/td/telegram/net/Session.cpp:459）。
	nearest := decodeInto[tg.NearestDC](t, "help.getNearestDc", bot.mustCall("help.getNearestDc", &tg.HelpGetNearestDCRequest{}))
	if nearest.ThisDC != srv.dc {
		t.Errorf("nearestDc.this_dc = %d, want %d", nearest.ThisDC, srv.dc)
	}

	ctx := context.Background()
	owner, err := srv.users.Create(ctx, domain.User{AccessHash: 9001, Phone: "+15550143001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	botUser, token, err := srv.bots.CreateBot(ctx, owner.ID, "TDLib Bot", "tdlib_contract_bot")
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}

	// auth.importBotAuthorization(flags=0, api_id, api_hash, bot_token)（td/td/telegram/AuthManager.cpp:480）。
	// telegram-bot-api 用运维随意填写的 api_id / api_hash，服务端不得校验应用凭据。
	authRaw := bot.mustCall("auth.importBotAuthorization", &tg.AuthImportBotAuthorizationRequest{
		APIID: 94575, APIHash: "0123456789abcdef0123456789abcdef", BotAuthToken: token,
	})
	authClass, err := tg.DecodeAuthAuthorization(authRaw)
	if err != nil {
		t.Fatalf("decode auth.authorization: %v", err)
	}
	authorization, ok := authClass.(*tg.AuthAuthorization)
	if !ok {
		t.Fatalf("auth.importBotAuthorization = %T, want auth.authorization", authClass)
	}
	// user 必须带 self 标记，否则 TDLib 报错并登出（AuthManager.cpp:1514-1530）。
	self, ok := authorization.User.(*tg.User)
	if !ok || !self.Self || !self.Bot || self.ID != botUser.ID {
		t.Fatalf("auth.authorization.user = %#v, want self bot %d", authorization.User, botUser.ID)
	}
	// TDLib 读取 tmp_sessions（AuthManager.cpp:1532），同样不能 > 1。
	if n, ok := authorization.GetTmpSessions(); ok && n > 1 {
		t.Errorf("auth.authorization.tmp_sessions = %d, want <= 1", n)
	}

	// 登录后 updates.getState → updates.getDifference(pts, date, qts)，不带 pts_limit / qts_limit
	// （td/td/telegram/UpdatesManager.cpp:135,200,496-528）。
	state := decodeInto[tg.UpdatesState](t, "updates.getState", bot.mustCall("updates.getState", &tg.UpdatesGetStateRequest{}))
	diffRaw := bot.mustCall("updates.getDifference", &tg.UpdatesGetDifferenceRequest{Pts: state.Pts, Date: state.Date, Qts: state.Qts})
	diff, err := tg.DecodeUpdatesDifference(diffRaw)
	if err != nil {
		t.Fatalf("decode updates.Difference: %v", err)
	}
	switch d := diff.(type) {
	case *tg.UpdatesDifferenceEmpty:
		// 从刚取得的 state 出发，无差异是正确结果。
	case *tg.UpdatesDifference:
		if d.State.Pts < state.Pts || d.State.Qts < state.Qts {
			t.Errorf("updates.difference state %+v regresses from %+v", d.State, state)
		}
	default:
		t.Errorf("updates.getDifference from fresh state = %T, want differenceEmpty or difference", diff)
	}
	// Bot API 的 getMe → users.getUsers([inputUserSelf])（telegram-bot-api/Client.cpp:9436）。
	usersRaw := bot.mustCall("users.getUsers", &tg.UsersGetUsersRequest{ID: []tg.InputUserClass{&tg.InputUserSelf{}}})
	gotUsers := decodeInto[tg.UserClassVector](t, "users.getUsers", usersRaw)
	if len(gotUsers.Elems) != 1 {
		t.Fatalf("users.getUsers(self) = %d users, want 1", len(gotUsers.Elems))
	}
	if me, ok := gotUsers.Elems[0].(*tg.User); !ok || !me.Self || me.ID != botUser.ID {
		t.Fatalf("users.getUsers(self) = %#v, want self bot %d", gotUsers.Elems[0], botUser.ID)
	}

	assertTDLibBotChannelDifference(t, srv, bot, owner.ID, botUser.ID)

	// TDLib 作为 bot 从不刷新 file_reference（td/td/telegram/files/FileReferenceManager.cpp:127,156），
	// 收到 FILE_REFERENCE_* 时下载直接失败。空的与过期的 file_reference 都必须能下载。
	for _, ref := range [][]byte{{}, []byte("stale-file-reference")} {
		raw := bot.mustCall("upload.getFile", &tg.UploadGetFileRequest{
			Location: &tg.InputDocumentFileLocation{ID: 777001, AccessHash: 1, FileReference: ref},
			Offset:   0,
			Limit:    512 * 1024,
		})
		file, err := tg.DecodeUploadFile(raw)
		if err != nil {
			t.Fatalf("decode upload.File: %v", err)
		}
		got, ok := file.(*tg.UploadFile)
		if !ok || string(got.Bytes) != "tdlib-bot-file" {
			t.Fatalf("upload.getFile(file_reference=%q) = %#v, want file bytes", ref, file)
		}
	}

	// help.getCdnConfig 周期性调用（td/td/telegram/net/PublicRsaKeyWatchdog.cpp:80、
	// UpdatesManager.cpp:181）。本服务没有 CDN DC：返回空 cdnConfig，TDLib 就不会去连 CDN。
	cdn := decodeInto[tg.CDNConfig](t, "help.getCdnConfig", bot.mustCall("help.getCdnConfig", &tg.HelpGetCDNConfigRequest{}))
	if len(cdn.PublicKeys) != 0 {
		t.Errorf("help.getCdnConfig public_keys = %+v, want empty", cdn.PublicKeys)
	}
	// help.setBotUpdatesStatus 由 telegram-bot-api 周期上报积压（td/td/telegram/BotQueries.cpp:81，
	// telegram-bot-api/Client.cpp:17136,17148）；返回 boolTrue 即可，服务端不保存。
	statusRaw := bot.mustCall("help.setBotUpdatesStatus", &tg.HelpSetBotUpdatesStatusRequest{PendingUpdatesCount: 3, Message: "webhook unreachable"})
	if status, err := tg.DecodeBool(statusRaw); err != nil {
		t.Fatalf("decode help.setBotUpdatesStatus: %v", err)
	} else if _, ok := status.(*tg.BoolTrue); !ok {
		t.Errorf("help.setBotUpdatesStatus = %T, want boolTrue", status)
	}

	assertTDLibBotPlaceholderMethods(t, bot)
}

// assertTDLibBotPlaceholderMethods：TDLib 会调用、本服务尚未支持的方法（#143）接在 Router 上。
// 各方法的返回形态由 rpc 层的 tdlib_bot_placeholders_test.go 覆盖，这里只证明经线上协议可达。
func assertTDLibBotPlaceholderMethods(t *testing.T, bot *tdlibBotConn) {
	t.Helper()
	// bots.setJoinChatResults：bot 审核入群申请（td/td/telegram/DialogParticipantManager.cpp:178）。
	raw := bot.mustCall("bots.setJoinChatResults", &tg.BotsSetJoinChatResultsRequest{
		QueryID: 1, Result: &tg.JoinChatBotResultApproved{},
	})
	if v, err := tg.DecodeBool(raw); err != nil {
		t.Fatalf("decode bots.setJoinChatResults: %v", err)
	} else if _, ok := v.(*tg.BoolTrue); !ok {
		t.Errorf("bots.setJoinChatResults = %T, want boolTrue", v)
	}
}

// assertTDLibBotChannelDifference：bot 的 updates.getChannelDifference 用
// channelMessagesFilterEmpty 和 limit=100000（td/td/telegram/MessagesManager.h:1676-1678、
// MessagesManager.cpp:2253,34893）。线上协议接受这个 limit，差距大于服务端单页时返回
// final=false 的 channelDifference 让 TDLib 接着拉，而不是 channelDifferenceTooLong。
// 分页本身（每页条数、pts 推进、末页 final）由 store 测试覆盖。
func assertTDLibBotChannelDifference(t *testing.T, srv tdlibContractServer, bot *tdlibBotConn, ownerID, botID int64) {
	t.Helper()
	ctx := context.Background()
	created, err := srv.channels.CreateMegagroupFromCreateChat(ctx, ownerID, domain.CreateChannelRequest{
		Title: "TDLib Contract Group", MemberUserIDs: []int64{botID}, Date: int(time.Now().Unix()),
	})
	if err != nil {
		t.Fatalf("create megagroup: %v", err)
	}
	channel := created.Channel
	startPts := channel.Pts
	const sent = domain.MaxChannelDifferenceLimit + 20
	for i := 0; i < sent; i++ {
		res, err := srv.channels.SendMessage(ctx, ownerID, domain.SendChannelMessageRequest{
			UserID: ownerID, ChannelID: channel.ID, RandomID: int64(1000 + i),
			Message: "/ping", Date: int(time.Now().Unix()),
		})
		if err != nil {
			t.Fatalf("send channel message %d: %v", i, err)
		}
		channel = res.Channel
	}
	if channel.Pts-startPts != sent {
		t.Fatalf("channel pts advanced %d, want %d", channel.Pts-startPts, sent)
	}
	input := &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	getDiff := func(pts int) tg.UpdatesChannelDifferenceClass {
		t.Helper()
		raw := bot.mustCall("updates.getChannelDifference", &tg.UpdatesGetChannelDifferenceRequest{
			Channel: input, Filter: &tg.ChannelMessagesFilterEmpty{}, Pts: pts, Limit: 100000,
		})
		out, err := tg.DecodeUpdatesChannelDifference(raw)
		if err != nil {
			t.Fatalf("decode updates.ChannelDifference: %v", err)
		}
		return out
	}

	firstRaw := getDiff(startPts)
	// final=false 时 TDLib 立即再次请求（MessagesManager.cpp 中 on_get_channel_difference 对 !is_final 的处理）。
	if first, ok := firstRaw.(*tg.UpdatesChannelDifference); !ok || first.Final {
		t.Fatalf("getChannelDifference(limit=100000) = %#v, want an updates.channelDifference page with final=false", firstRaw)
	}
}

// TestTDLibBotContractQtsDifferenceAfterOffline：bot 离线期间自己在超级群里的成员变化，重连后
// 由 updates.getDifference 按 qts 顺序补回。TDLib 把 updateChannelParticipant 当作 qts 类更新
// （td/td/telegram/UpdatesManager.cpp:4254 is_qts_update），用登录时 updates.getState 拿到的 qts
// 发 getDifference（UpdatesManager.cpp:200），并从 difference.state.qts 推进本地 qts
// （UpdatesManager.cpp:1354 on_get_updates_state）。
func TestTDLibBotContractQtsDifferenceAfterOffline(t *testing.T) {
	t.Parallel()
	srv := startTDLibContractServer(t)
	ctx := context.Background()
	owner, err := srv.users.Create(ctx, domain.User{AccessHash: 9002, Phone: "+15550143002", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	botUser, token, err := srv.bots.CreateBot(ctx, owner.ID, "TDLib Qts Bot", "tdlib_qts_bot")
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	login := func() *tdlibBotConn {
		t.Helper()
		bot := dialTDLibBot(t, srv.addr, srv.dc, srv.pub)
		bot.mustCall("invokeWithLayer(initConnection(help.getConfig))", tdlibInitConnection(&tg.HelpGetConfigRequest{}))
		bot.mustCall("auth.importBotAuthorization", &tg.AuthImportBotAuthorizationRequest{
			APIID: 94575, APIHash: "0123456789abcdef0123456789abcdef", BotAuthToken: token,
		})
		return bot
	}

	first := login()
	state := decodeInto[tg.UpdatesState](t, "updates.getState", first.mustCall("updates.getState", &tg.UpdatesGetStateRequest{}))
	first.conn.Close()

	// 离线期间：bot 被拉进群，随后被设为管理员。
	created, err := srv.channels.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{Title: "TDLib Qts Group", Megagroup: true, Date: int(time.Now().Unix())})
	if err != nil {
		t.Fatalf("create megagroup: %v", err)
	}
	if _, err := srv.channels.InviteToChannel(ctx, owner.ID, created.Channel.ID, []int64{botUser.ID}, int(time.Now().Unix())); err != nil {
		t.Fatalf("invite bot: %v", err)
	}
	if _, err := srv.channels.EditAdmin(ctx, owner.ID, domain.EditChannelAdminRequest{
		ChannelID: created.Channel.ID, MemberID: botUser.ID, AdminRights: domain.ChannelAdminRights{DeleteMessages: true},
	}); err != nil {
		t.Fatalf("promote bot: %v", err)
	}

	second := login()
	raw := second.mustCall("updates.getDifference", &tg.UpdatesGetDifferenceRequest{Pts: state.Pts, Date: state.Date, Qts: state.Qts})
	diffClass, err := tg.DecodeUpdatesDifference(raw)
	if err != nil {
		t.Fatalf("decode updates.Difference: %v", err)
	}
	// qts 类补差永远不能是 differenceTooLong：TDLib 收到它只 set_pts，然后用同一个旧 qts 再次
	// getDifference（td/td/telegram/UpdatesManager.cpp:2168-2176），qts 落后于保留期时会无限循环。
	// 服务端因此从最旧的保留记录补起，differenceTooLong 只留给 pts 的 pts_total_limit 场景。
	if _, tooLong := diffClass.(*tg.UpdatesDifferenceTooLong); tooLong {
		t.Fatalf("updates.getDifference for bot qts returned differenceTooLong; TDLib would loop on the stale qts")
	}
	diff, ok := diffClass.(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("updates.getDifference after offline = %T, want updates.difference", diffClass)
	}
	var participants []*tg.UpdateChannelParticipant
	for _, update := range diff.OtherUpdates {
		if p, ok := update.(*tg.UpdateChannelParticipant); ok {
			participants = append(participants, p)
		}
	}
	if len(participants) != 2 || participants[0].Qts != state.Qts+1 || participants[1].Qts != state.Qts+2 {
		t.Fatalf("qts updates = %+v, want two updateChannelParticipant at qts %d,%d", participants, state.Qts+1, state.Qts+2)
	}
	for _, p := range participants {
		if p.ChannelID != created.Channel.ID || p.UserID != botUser.ID || p.ActorID != owner.ID {
			t.Fatalf("updateChannelParticipant %+v, want bot %d in channel %d by owner %d", p, botUser.ID, created.Channel.ID, owner.ID)
		}
	}
	if newParticipant, _ := participants[1].GetNewParticipant(); newParticipant == nil {
		t.Fatalf("promotion update carries no new participant: %+v", participants[1])
	} else if _, ok := newParticipant.(*tg.ChannelParticipantAdmin); !ok {
		t.Fatalf("promotion new participant = %T, want channelParticipantAdmin", newParticipant)
	}
	if diff.State.Qts != state.Qts+2 {
		t.Fatalf("difference state.qts = %d, want %d", diff.State.Qts, state.Qts+2)
	}
	// updateChannelParticipant 引用的频道必须随差分下发，TDLib 才能解析 channel_id。
	foundChannel := false
	for _, chat := range diff.Chats {
		if ch, ok := chat.(*tg.Channel); ok && ch.ID == created.Channel.ID {
			foundChannel = true
		}
	}
	if !foundChannel {
		t.Fatalf("difference chats %+v lack channel %d", diff.Chats, created.Channel.ID)
	}
}
