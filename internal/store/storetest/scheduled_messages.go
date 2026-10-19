package storetest

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// scheduledFarFuture 是契约里"不该到期"的定时行的排程偏移：到期认领是全局扫描，
// 除到期认领小节专门造的行之外，其余行都排在远未来，不会被任何一次认领扫到。
const scheduledFarFuture = 100000

// ScheduledMessages 校验两套实现对定时消息的同一契约：按 owner+random_id 幂等、
// 列表/获取按 owner 隔离、状态 pending→dispatching→sent/deleted、手动与到期认领的
// 租约与重试退避、编辑与删除、以及到点发出所需的字段（回复、send_as、转发头、相册
// grouped_id）原样往返。
//
// ClaimDueScheduledMessages 是全局扫描：调用方必须给一个只属于本测试的 store
// （postgres 侧用 pgtest.NewDatabase 独占库）。owner、other 是两个账号，peer 是
// owner/other 私聊的对端用户；频道 peer 用 channelBase 起的任意 id。
func ScheduledMessages(t *testing.T, s store.ScheduledMessageStore, owner, other, peer int64, channelBase int64) {
	t.Helper()
	ctx := context.Background()
	base := baseNow()
	far := base + scheduledFarFuture
	userPeer := domain.Peer{Type: domain.PeerTypeUser, ID: peer}
	channel := func(n int64) domain.Peer { return domain.Peer{Type: domain.PeerTypeChannel, ID: channelBase + n} }

	create := func(t *testing.T, req domain.ScheduleMessageRequest) domain.ScheduledMessage {
		t.Helper()
		if req.Date == 0 {
			req.Date = base
		}
		msg, err := s.CreateScheduledMessage(ctx, req)
		if err != nil {
			t.Fatalf("CreateScheduledMessage(%+v): %v", req, err)
		}
		return msg
	}
	list := func(t *testing.T, filter domain.ScheduledMessageFilter) domain.ScheduledMessageList {
		t.Helper()
		out, err := s.ListScheduledMessages(ctx, filter)
		if err != nil {
			t.Fatalf("ListScheduledMessages: %v", err)
		}
		return out
	}
	get := func(t *testing.T, filter domain.ScheduledMessageFilter) []domain.ScheduledMessage {
		t.Helper()
		out, err := s.GetScheduledMessages(ctx, filter)
		if err != nil {
			t.Fatalf("GetScheduledMessages: %v", err)
		}
		return out.Messages
	}
	claimDue := func(t *testing.T, now, limit, lease int) []domain.ScheduledMessage {
		t.Helper()
		out, err := s.ClaimDueScheduledMessages(ctx, now, limit, lease)
		if err != nil {
			t.Fatalf("ClaimDueScheduledMessages(now=%d): %v", now, err)
		}
		return out
	}
	has := func(t *testing.T, owner int64, peer domain.Peer) bool {
		t.Helper()
		ok, err := s.HasScheduledMessages(ctx, owner, peer)
		if err != nil {
			t.Fatalf("HasScheduledMessages: %v", err)
		}
		return ok
	}

	t.Run("CreateIdempotentPerOwner", func(t *testing.T) {
		for _, bad := range []domain.ScheduleMessageRequest{
			{Peer: userPeer, Message: "x", ScheduleDate: far},
			{OwnerUserID: owner, Peer: domain.Peer{Type: domain.PeerTypeCommunity, ID: 5}, Message: "x", ScheduleDate: far},
			{OwnerUserID: owner, Peer: userPeer, ScheduleDate: far},
			{OwnerUserID: owner, Peer: userPeer, Message: "x"},
		} {
			if _, err := s.CreateScheduledMessage(ctx, bad); err == nil {
				t.Fatalf("CreateScheduledMessage(%+v) succeeded, want error", bad)
			}
		}
		first := create(t, domain.ScheduleMessageRequest{OwnerUserID: owner, Peer: userPeer, RandomID: 101, Message: "first", ScheduleDate: far + 20, Date: base + 1})
		if first.ID != 1 || first.OwnerUserID != owner || first.State != "pending" || first.CreatedAt != base+1 || first.UpdatedAt != base+1 || first.ScheduleDate != far+20 {
			t.Fatalf("first scheduled = %+v, want id 1 pending at date %d", first, base+1)
		}
		dup := create(t, domain.ScheduleMessageRequest{OwnerUserID: owner, Peer: userPeer, RandomID: 101, Message: "replayed", ScheduleDate: far + 99})
		if dup.ID != first.ID || dup.Message != "first" || dup.ScheduleDate != far+20 {
			t.Fatalf("replayed random_id = %+v, want the original row %d unchanged", dup, first.ID)
		}
		second := create(t, domain.ScheduleMessageRequest{OwnerUserID: owner, Peer: userPeer, RandomID: 102, Message: "second", ScheduleDate: far + 10})
		if second.ID != 2 {
			t.Fatalf("second scheduled id = %d, want 2", second.ID)
		}
		noRandom := create(t, domain.ScheduleMessageRequest{OwnerUserID: owner, Peer: userPeer, Message: "no random", ScheduleDate: far + 30})
		noRandomAgain := create(t, domain.ScheduleMessageRequest{OwnerUserID: owner, Peer: userPeer, Message: "no random", ScheduleDate: far + 30})
		if noRandom.ID == noRandomAgain.ID {
			t.Fatalf("random_id 0 deduplicated to %d, want two rows", noRandom.ID)
		}
		theirs := create(t, domain.ScheduleMessageRequest{OwnerUserID: other, Peer: userPeer, RandomID: 101, Message: "theirs", ScheduleDate: far + 5})
		if theirs.ID != 1 || theirs.OwnerUserID != other || theirs.Message != "theirs" {
			t.Fatalf("other owner's same random_id = %+v, want its own row id 1", theirs)
		}
		if !has(t, owner, userPeer) || has(t, owner, channel(1)) || has(t, 0, userPeer) {
			t.Fatalf("HasScheduledMessages mismatch after create")
		}
	})

	t.Run("ListAndGetByOwner", func(t *testing.T) {
		mine := list(t, domain.ScheduledMessageFilter{OwnerUserID: owner, Peer: userPeer})
		if ids := scheduledIDs(mine.Messages); !reflect.DeepEqual(ids, []int{2, 1, 3, 4}) || mine.Count != 4 || mine.Hash == 0 {
			t.Fatalf("owner list ids = %v count %d hash %d, want [2 1 3 4] ordered by schedule_date then id", ids, mine.Count, mine.Hash)
		}
		for _, msg := range mine.Messages {
			if msg.OwnerUserID != owner {
				t.Fatalf("owner list leaked %+v", msg)
			}
		}
		if again := list(t, domain.ScheduledMessageFilter{OwnerUserID: owner, Peer: userPeer, Hash: mine.Hash}); again.Messages != nil || again.Count != 0 || again.Hash != mine.Hash {
			t.Fatalf("list with matching hash = %+v, want not-modified (no messages, same hash)", again)
		}
		if limited := list(t, domain.ScheduledMessageFilter{OwnerUserID: owner, Peer: userPeer, Limit: 1}); !reflect.DeepEqual(scheduledIDs(limited.Messages), []int{2}) {
			t.Fatalf("limited list = %v, want [2]", scheduledIDs(limited.Messages))
		}
		theirs := list(t, domain.ScheduledMessageFilter{OwnerUserID: other, Peer: userPeer})
		if len(theirs.Messages) != 1 || theirs.Messages[0].Message != "theirs" {
			t.Fatalf("other list = %+v, want only the other owner's row", theirs.Messages)
		}
		if empty := list(t, domain.ScheduledMessageFilter{Peer: userPeer}); len(empty.Messages) != 0 {
			t.Fatalf("list without owner = %+v, want empty", empty.Messages)
		}
		if got := get(t, domain.ScheduledMessageFilter{OwnerUserID: owner, Peer: userPeer, IDs: []int{1, 999, -1}}); !reflect.DeepEqual(scheduledIDs(got), []int{1}) || got[0].Message != "first" {
			t.Fatalf("get [1 999 -1] = %+v, want only row 1", got)
		}
		if got := get(t, domain.ScheduledMessageFilter{OwnerUserID: other, Peer: userPeer, IDs: []int{2}}); len(got) != 0 {
			t.Fatalf("other owner got owner's row 2: %+v", got)
		}
		if got := get(t, domain.ScheduledMessageFilter{OwnerUserID: owner, Peer: channel(1), IDs: []int{1}}); len(got) != 0 {
			t.Fatalf("get on another peer = %+v, want empty", got)
		}
		if got := get(t, domain.ScheduledMessageFilter{OwnerUserID: owner, Peer: userPeer}); len(got) != 0 {
			t.Fatalf("get without ids = %+v, want empty", got)
		}
	})

	t.Run("Edit", func(t *testing.T) {
		peer := channel(1)
		edit := func(t *testing.T, req domain.EditScheduledMessageRequest) (domain.ScheduledMessage, error) {
			t.Helper()
			req.OwnerUserID, req.Peer = owner, peer
			return s.EditScheduledMessage(ctx, req)
		}
		media := &domain.MessageMedia{
			Kind: domain.MessageMediaKindDocument,
			Document: &domain.Document{
				ID: 910000000000000101, AccessHash: 9101, DCID: 2, MimeType: "application/x-tgsticker",
				Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker, Alt: "wave"}},
			},
		}
		withMedia := create(t, domain.ScheduleMessageRequest{OwnerUserID: owner, Peer: peer, RandomID: 201, Message: "caption", Media: media, ScheduleDate: far})
		dateOnly, err := edit(t, domain.EditScheduledMessageRequest{ID: withMedia.ID, ScheduleDate: far + 1, Date: base + 10})
		if err != nil {
			t.Fatalf("date-only edit: %v", err)
		}
		if dateOnly.Message != "caption" || !sameDocument(dateOnly.Media, media) || dateOnly.ScheduleDate != far+1 || dateOnly.UpdatedAt != base+10 || dateOnly.CreatedAt != base {
			t.Fatalf("date-only edit = %+v, want caption and media kept, new date", dateOnly)
		}
		cleared, err := edit(t, domain.EditScheduledMessageRequest{ID: withMedia.ID, SetMessage: true, ScheduleDate: far + 2, Date: base + 11})
		if err != nil {
			t.Fatalf("empty-caption edit of media: %v", err)
		}
		if cleared.Message != "" || !sameDocument(cleared.Media, media) || cleared.ScheduleDate != far+2 {
			t.Fatalf("empty-caption edit = %+v, want caption cleared and media kept", cleared)
		}
		entities := []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 3}}
		recaptioned, err := edit(t, domain.EditScheduledMessageRequest{ID: withMedia.ID, SetMessage: true, Message: "new", Entities: entities, ScheduleDate: far + 3, Date: base + 12})
		if err != nil {
			t.Fatalf("caption edit: %v", err)
		}
		if got := get(t, domain.ScheduledMessageFilter{OwnerUserID: owner, Peer: peer, IDs: []int{withMedia.ID}}); len(got) != 1 || got[0].Message != "new" || !reflect.DeepEqual(got[0].Entities, entities) || got[0].ScheduleDate != far+3 || !reflect.DeepEqual(got[0], recaptioned) {
			t.Fatalf("get after caption edit = %+v, want the edited row %+v", got, recaptioned)
		}

		text := create(t, domain.ScheduleMessageRequest{OwnerUserID: owner, Peer: peer, RandomID: 202, Message: "text", ScheduleDate: far})
		if _, err := edit(t, domain.EditScheduledMessageRequest{ID: text.ID, SetMessage: true, ScheduleDate: far, Date: base + 13}); !errors.Is(err, domain.ErrMessageEmpty) {
			t.Fatalf("emptying a text-only row err = %v, want ErrMessageEmpty", err)
		}

		rich := &domain.MessageRichMessage{Rtl: true, Blocks: []byte{1, 2, 3, 4}}
		richRow := create(t, domain.ScheduleMessageRequest{OwnerUserID: owner, Peer: peer, RandomID: 203, RichMessage: rich, ScheduleDate: far})
		if !reflect.DeepEqual(richRow.RichMessage, rich) || richRow.Message != "" || richRow.Media != nil {
			t.Fatalf("rich-only create = %+v, want rich-only content", richRow)
		}
		richDateOnly, err := edit(t, domain.EditScheduledMessageRequest{ID: richRow.ID, ScheduleDate: far + 4, Date: base + 14})
		if err != nil || !reflect.DeepEqual(richDateOnly.RichMessage, rich) {
			t.Fatalf("date-only edit of rich = %+v err %v, want rich kept", richDateOnly, err)
		}
		replacement := &domain.MessageRichMessage{Part: true, Blocks: []byte{5, 6, 7}}
		richEdited, err := edit(t, domain.EditScheduledMessageRequest{ID: richRow.ID, SetRichMessage: true, RichMessage: replacement, ScheduleDate: far + 5, Date: base + 15})
		if err != nil || !reflect.DeepEqual(richEdited.RichMessage, replacement) {
			t.Fatalf("rich edit = %+v err %v, want replacement", richEdited, err)
		}
		if _, err := edit(t, domain.EditScheduledMessageRequest{ID: richRow.ID, SetRichMessage: true, ScheduleDate: far + 6, Date: base + 16}); !errors.Is(err, domain.ErrMessageEmpty) {
			t.Fatalf("clearing the only rich content err = %v, want ErrMessageEmpty", err)
		}

		if _, err := edit(t, domain.EditScheduledMessageRequest{ID: 999, ScheduleDate: far, Date: base}); !errors.Is(err, domain.ErrMessageIDInvalid) {
			t.Fatalf("edit of a missing row err = %v, want ErrMessageIDInvalid", err)
		}
		if _, err := s.EditScheduledMessage(ctx, domain.EditScheduledMessageRequest{OwnerUserID: other, Peer: peer, ID: text.ID, ScheduleDate: far, Date: base}); !errors.Is(err, domain.ErrMessageIDInvalid) {
			t.Fatalf("edit of another owner's row err = %v, want ErrMessageIDInvalid", err)
		}
		if _, err := s.ClaimScheduledMessages(ctx, domain.ScheduledMessageClaim{OwnerUserID: owner, Peer: peer, IDs: []int{text.ID}, Now: base, LeaseUntil: base + 60}); err != nil {
			t.Fatalf("claim before edit: %v", err)
		}
		if _, err := edit(t, domain.EditScheduledMessageRequest{ID: text.ID, SetMessage: true, Message: "late", ScheduleDate: far, Date: base}); !errors.Is(err, domain.ErrMessageIDInvalid) {
			t.Fatalf("edit of a dispatching row err = %v, want ErrMessageIDInvalid", err)
		}
	})

	t.Run("DeleteKeepsIDsMonotonic", func(t *testing.T) {
		peer := channel(2)
		a := create(t, domain.ScheduleMessageRequest{OwnerUserID: owner, Peer: peer, RandomID: 301, Message: "a", ScheduleDate: far})
		b := create(t, domain.ScheduleMessageRequest{OwnerUserID: owner, Peer: peer, RandomID: 302, Message: "b", ScheduleDate: far})
		if _, err := s.ClaimScheduledMessages(ctx, domain.ScheduledMessageClaim{OwnerUserID: owner, Peer: peer, IDs: []int{b.ID}, Now: base, LeaseUntil: base + 60}); err != nil {
			t.Fatalf("claim b: %v", err)
		}
		if deleted, err := s.DeleteScheduledMessages(ctx, domain.ScheduledMessageFilter{OwnerUserID: other, Peer: peer, IDs: []int{a.ID}}, base+20); err != nil || len(deleted) != 0 {
			t.Fatalf("other owner delete = %+v err %v, want nothing", deleted, err)
		}
		deleted, err := s.DeleteScheduledMessages(ctx, domain.ScheduledMessageFilter{OwnerUserID: owner, Peer: peer, IDs: []int{a.ID, b.ID, 999}}, base+21)
		if err != nil {
			t.Fatalf("delete: %v", err)
		}
		if ids := scheduledIDs(deleted); len(ids) != 2 {
			t.Fatalf("deleted = %+v, want pending a and dispatching b", deleted)
		}
		for _, row := range deleted {
			if row.State != "deleted" || row.UpdatedAt != base+21 {
				t.Fatalf("deleted row = %+v, want state deleted at %d", row, base+21)
			}
		}
		if again, err := s.DeleteScheduledMessages(ctx, domain.ScheduledMessageFilter{OwnerUserID: owner, Peer: peer, IDs: []int{a.ID}}, base+22); err != nil || len(again) != 0 {
			t.Fatalf("second delete = %+v err %v, want nothing", again, err)
		}
		if rows := list(t, domain.ScheduledMessageFilter{OwnerUserID: owner, Peer: peer}); len(rows.Messages) != 0 || has(t, owner, peer) {
			t.Fatalf("list after delete = %+v, want empty queue", rows.Messages)
		}
		reborn := create(t, domain.ScheduleMessageRequest{OwnerUserID: owner, Peer: peer, RandomID: 301, Message: "a again", ScheduleDate: far})
		if reborn.ID <= b.ID || reborn.Message != "a again" {
			t.Fatalf("recreated after delete = %+v, want a new row above %d (ids never reused, random_id free again)", reborn, b.ID)
		}
	})

	t.Run("ManualClaimLeaseReleaseAndSend", func(t *testing.T) {
		peer := channel(3)
		row := create(t, domain.ScheduleMessageRequest{OwnerUserID: owner, Peer: peer, RandomID: 401, Message: "claim me", ScheduleDate: far})
		claim := func(t *testing.T, ownerID int64, now int) []domain.ScheduledMessage {
			t.Helper()
			out, err := s.ClaimScheduledMessages(ctx, domain.ScheduledMessageClaim{OwnerUserID: ownerID, Peer: peer, IDs: []int{row.ID}, Now: now, LeaseUntil: now + 60})
			if err != nil {
				t.Fatalf("ClaimScheduledMessages: %v", err)
			}
			return out
		}
		if got := claim(t, other, base); len(got) != 0 {
			t.Fatalf("other owner claimed %+v", got)
		}
		first := claim(t, owner, base)
		if len(first) != 1 || first[0].State != "dispatching" || first[0].UpdatedAt != base {
			t.Fatalf("claim = %+v, want the row dispatching", first)
		}
		if got := claim(t, owner, base+30); len(got) != 0 {
			t.Fatalf("claim under an active lease = %+v, want nothing", got)
		}
		if rows := list(t, domain.ScheduledMessageFilter{OwnerUserID: owner, Peer: peer}); len(rows.Messages) != 1 || rows.Messages[0].State != "dispatching" {
			t.Fatalf("list while dispatching = %+v, want the row still queued", rows.Messages)
		}
		if dup := create(t, domain.ScheduleMessageRequest{OwnerUserID: owner, Peer: peer, RandomID: 401, Message: "retry", ScheduleDate: far}); dup.ID != row.ID {
			t.Fatalf("replay while dispatching = %+v, want row %d", dup, row.ID)
		}
		if got := claim(t, owner, base+60); len(got) != 1 {
			t.Fatalf("claim after lease expiry = %+v, want the row reclaimed", got)
		}
		if err := s.ReleaseScheduledMessage(ctx, owner, row.ID, "boom"); err != nil {
			t.Fatalf("release: %v", err)
		}
		if got := get(t, domain.ScheduledMessageFilter{OwnerUserID: owner, Peer: peer, IDs: []int{row.ID}}); len(got) != 1 || got[0].State != "pending" {
			t.Fatalf("after release = %+v, want pending", got)
		}
		if got := claim(t, owner, base+61); len(got) != 1 {
			t.Fatalf("manual claim after release = %+v, want it claimable at once", got)
		}
		if err := s.MarkScheduledMessageSent(ctx, owner, row.ID, 5550, base+62); err != nil {
			t.Fatalf("mark sent: %v", err)
		}
		if got := get(t, domain.ScheduledMessageFilter{OwnerUserID: owner, Peer: peer, IDs: []int{row.ID}}); len(got) != 0 || has(t, owner, peer) {
			t.Fatalf("sent row still queued: %+v", got)
		}
		if got := claim(t, owner, base+200); len(got) != 0 {
			t.Fatalf("claimed a sent row: %+v", got)
		}
		if err := s.ReleaseScheduledMessage(ctx, owner, row.ID, "late"); err != nil {
			t.Fatalf("release of a sent row: %v", err)
		}
		if got := get(t, domain.ScheduledMessageFilter{OwnerUserID: owner, Peer: peer, IDs: []int{row.ID}}); len(got) != 0 {
			t.Fatalf("release resurrected a sent row: %+v", got)
		}
		resent := create(t, domain.ScheduleMessageRequest{OwnerUserID: owner, Peer: peer, RandomID: 401, Message: "again", ScheduleDate: far})
		if resent.ID == row.ID {
			t.Fatalf("random_id of a sent row still deduplicated to %d", row.ID)
		}
	})

	t.Run("ClaimDueHonoursDateLeaseAndBackoff", func(t *testing.T) {
		peer := channel(4)
		if got := claimDue(t, 0, 10, 5); len(got) != 0 {
			t.Fatalf("claim due at now=0 = %+v, want nothing", got)
		}
		d1 := create(t, domain.ScheduleMessageRequest{OwnerUserID: owner, Peer: peer, RandomID: 501, Message: "d1", ScheduleDate: base - 50})
		d3 := create(t, domain.ScheduleMessageRequest{OwnerUserID: other, Peer: peer, RandomID: 503, Message: "d3", ScheduleDate: base - 45})
		d2 := create(t, domain.ScheduleMessageRequest{OwnerUserID: owner, Peer: peer, RandomID: 502, Message: "d2", ScheduleDate: base - 40})
		later := create(t, domain.ScheduleMessageRequest{OwnerUserID: owner, Peer: peer, RandomID: 504, Message: "later", ScheduleDate: base + 5})

		key := func(m domain.ScheduledMessage) [2]int64 { return [2]int64{m.OwnerUserID, int64(m.ID)} }
		// 认领结果的返回顺序不属于契约（postgres 的 UPDATE … RETURNING 不保证顺序），
		// 比较前按 (owner, id) 排序；被选中的是哪几条才由 schedule_date 顺序与 limit 决定。
		keys := func(rows []domain.ScheduledMessage) [][2]int64 {
			out := make([][2]int64, 0, len(rows))
			for _, row := range rows {
				out = append(out, key(row))
			}
			sort.Slice(out, func(i, j int) bool {
				if out[i][0] != out[j][0] {
					return out[i][0] < out[j][0]
				}
				return out[i][1] < out[j][1]
			})
			return out
		}
		sortedKeys := func(rows ...domain.ScheduledMessage) [][2]int64 { return keys(rows) }
		first := claimDue(t, base, 2, 5)
		if got, want := keys(first), sortedKeys(d1, d3); !reflect.DeepEqual(got, want) {
			t.Fatalf("first due batch = %v, want %v (schedule_date order, limit 2)", got, want)
		}
		for _, row := range first {
			if row.State != "dispatching" || row.UpdatedAt != base {
				t.Fatalf("claimed due row = %+v, want dispatching at %d", row, base)
			}
		}
		if got, want := keys(claimDue(t, base, 100, 5)), sortedKeys(d2); !reflect.DeepEqual(got, want) {
			t.Fatalf("second due batch = %v, want %v", got, want)
		}
		if got := claimDue(t, base, 100, 5); len(got) != 0 {
			t.Fatalf("third due batch = %+v, want nothing under active leases", got)
		}
		// d1 投递失败（带错误）退避；d2 无错误释放可立即重领；d3 模拟派发器崩溃，租约过期后回收。
		if err := s.ReleaseScheduledMessage(ctx, owner, d1.ID, "peer gone"); err != nil {
			t.Fatalf("release d1: %v", err)
		}
		if err := s.ReleaseScheduledMessage(ctx, owner, d2.ID, ""); err != nil {
			t.Fatalf("release d2: %v", err)
		}
		if got, want := keys(claimDue(t, base+10, 100, 5)), sortedKeys(d3, d2, later); !reflect.DeepEqual(got, want) {
			t.Fatalf("due batch after release = %v, want %v (d1 backing off, expired d3 reclaimed, later row now due)", got, want)
		}
		for _, row := range []domain.ScheduledMessage{d2, d3, later} {
			if err := s.MarkScheduledMessageSent(ctx, row.OwnerUserID, row.ID, 7000+row.ID, base+11); err != nil {
				t.Fatalf("mark sent: %v", err)
			}
		}
		if got, want := keys(claimDue(t, base+40, 100, 5)), sortedKeys(d1); !reflect.DeepEqual(got, want) {
			t.Fatalf("due batch after backoff = %v, want %v", got, want)
		}
		if err := s.MarkScheduledMessageSent(ctx, owner, d1.ID, 7001, base+41); err != nil {
			t.Fatalf("mark d1 sent: %v", err)
		}
		if got := claimDue(t, base+1000, 100, 5); len(got) != 0 {
			t.Fatalf("due batch after everything sent = %+v, want nothing", got)
		}
	})

	t.Run("FieldsRoundTrip", func(t *testing.T) {
		peer := channel(5)
		sendAs := &domain.Peer{Type: domain.PeerTypeChannel, ID: peer.ID}
		reply := &domain.MessageReply{
			MessageID: 41, Peer: peer, TopMessageID: 40, QuoteText: "quoted",
			QuoteEntities: []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 6}},
			QuoteOffset:   3,
		}
		forward := &domain.MessageForward{
			From: domain.Peer{Type: domain.PeerTypeChannel, ID: channelBase + 99}, Date: base - 1000, ChannelPost: 77,
			SavedFrom: userPeer, SavedFromMsgID: 55,
		}
		topicOnly := &domain.MessageReply{Peer: peer, TopMessageID: 12, ForumTopic: true}
		entities := []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 4}}
		const groupedID = int64(7766554433221100)
		due := base - 30

		full := create(t, domain.ScheduleMessageRequest{
			OwnerUserID: owner, Peer: peer, RandomID: 601, Message: "full", Entities: entities,
			Silent: true, NoForwards: true, ReplyTo: reply, Forward: forward, SendAs: sendAs,
			ScheduleDate: due, ScheduleRepeatPeriod: 86400,
		})
		topic := create(t, domain.ScheduleMessageRequest{OwnerUserID: owner, Peer: peer, RandomID: 602, Message: "into topic", ReplyTo: topicOnly, ScheduleDate: due})
		album := make([]domain.ScheduledMessage, 0, 3)
		for i := int64(0); i < 3; i++ {
			album = append(album, create(t, domain.ScheduleMessageRequest{
				OwnerUserID: owner, Peer: peer, RandomID: 610 + i, GroupedID: groupedID, ScheduleDate: due,
				Media: &domain.MessageMedia{Kind: domain.MessageMediaKindDocument, Document: &domain.Document{ID: 910000000000000201 + i, AccessHash: 9201, DCID: 2, MimeType: "image/jpeg"}},
			}))
		}
		single := create(t, domain.ScheduleMessageRequest{OwnerUserID: owner, Peer: peer, RandomID: 619, Message: "single", ScheduleDate: far})

		check := func(t *testing.T, label string, got domain.ScheduledMessage) {
			t.Helper()
			switch got.RandomID {
			case 601:
				if got.Message != "full" || !reflect.DeepEqual(got.Entities, entities) || !got.Silent || !got.NoForwards || got.ScheduleRepeatPeriod != 86400 {
					t.Fatalf("%s: content/flags = %+v", label, got)
				}
				if !reflect.DeepEqual(got.ReplyTo, reply) {
					t.Fatalf("%s: reply = %+v, want %+v", label, got.ReplyTo, reply)
				}
				if !reflect.DeepEqual(got.Forward, forward) {
					t.Fatalf("%s: forward = %+v, want %+v", label, got.Forward, forward)
				}
				if !reflect.DeepEqual(got.SendAs, sendAs) || got.GroupedID != 0 {
					t.Fatalf("%s: send_as/grouped = %+v/%d, want %+v/0", label, got.SendAs, got.GroupedID, sendAs)
				}
			case 602:
				if !reflect.DeepEqual(got.ReplyTo, topicOnly) || got.Forward != nil || got.SendAs != nil {
					t.Fatalf("%s: topic-only reply = %+v fwd %+v send_as %+v, want %+v", label, got.ReplyTo, got.Forward, got.SendAs, topicOnly)
				}
			case 610, 611, 612:
				if got.GroupedID != groupedID || got.Media == nil || got.Media.Document == nil || got.Media.Document.ID != 910000000000000201+got.RandomID-610 {
					t.Fatalf("%s: album item = %+v, want grouped %d with its document", label, got, groupedID)
				}
			case 619:
				if got.GroupedID != 0 {
					t.Fatalf("%s: single grouped_id = %d, want 0", label, got.GroupedID)
				}
			default:
				t.Fatalf("%s: unexpected row %+v", label, got)
			}
		}
		checkAll := func(t *testing.T, label string, rows []domain.ScheduledMessage, want int) {
			t.Helper()
			if len(rows) != want {
				t.Fatalf("%s: %d rows, want %d (%+v)", label, len(rows), want, rows)
			}
			for _, row := range rows {
				check(t, label, row)
			}
		}
		checkAll(t, "create", append([]domain.ScheduledMessage{full, topic, single}, album...), 6)
		checkAll(t, "list", list(t, domain.ScheduledMessageFilter{OwnerUserID: owner, Peer: peer}).Messages, 6)
		checkAll(t, "get", get(t, domain.ScheduledMessageFilter{OwnerUserID: owner, Peer: peer, IDs: []int{full.ID, topic.ID, album[0].ID, album[1].ID}}), 4)

		edited, err := s.EditScheduledMessage(ctx, domain.EditScheduledMessageRequest{OwnerUserID: owner, Peer: peer, ID: full.ID, ScheduleDate: due, Date: base + 1})
		if err != nil {
			t.Fatalf("edit full: %v", err)
		}
		check(t, "edit", edited)
		captioned, err := s.EditScheduledMessage(ctx, domain.EditScheduledMessageRequest{OwnerUserID: owner, Peer: peer, ID: album[0].ID, SetMessage: true, Message: "caption", ScheduleDate: due, Date: base + 1})
		if err != nil {
			t.Fatalf("edit album caption: %v", err)
		}
		check(t, "edit album", captioned)

		claimed, err := s.ClaimScheduledMessages(ctx, domain.ScheduledMessageClaim{OwnerUserID: owner, Peer: peer, IDs: []int{topic.ID, album[0].ID}, Now: base - 100, LeaseUntil: base - 40})
		if err != nil {
			t.Fatalf("manual claim: %v", err)
		}
		checkAll(t, "claim", claimed, 2)
		// 手动认领的租约已在 base 之前过期，到期认领一并回收：五条到期行全在，远未来的单条不在。
		checkAll(t, "claim due", claimDue(t, base, 100, 60), 5)
		deleted, err := s.DeleteScheduledMessages(ctx, domain.ScheduledMessageFilter{OwnerUserID: owner, Peer: peer, IDs: []int{album[2].ID, single.ID}}, base+2)
		if err != nil {
			t.Fatalf("delete: %v", err)
		}
		checkAll(t, "delete", deleted, 2)
	})
}

func scheduledIDs(rows []domain.ScheduledMessage) []int {
	out := make([]int, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out
}

func sameDocument(got, want *domain.MessageMedia) bool {
	return got != nil && got.Document != nil && want.Document != nil && got.Kind == want.Kind &&
		got.Document.ID == want.Document.ID && reflect.DeepEqual(got.Document.Attributes, want.Document.Attributes)
}
