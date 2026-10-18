package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres"
)

// The audit viewer is hand-written SQL over message_boxes, channel_messages,
// dialogs and the channel member index. The data is produced through the real
// stores -- sends, deletes, channel creation -- so the dialogs, membership and
// deletion rows are the ones production writes; only album and reply fields
// are patched in afterwards.
func TestReadStoreMessageAudit(t *testing.T) {
	store, pool := verificationReadStore(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%06d", time.Now().UnixNano()%1_000_000)
	users := postgres.NewUserStore(pool)
	newUser := func(name string, n int) domain.User {
		t.Helper()
		u, err := users.Create(ctx, domain.User{AccessHash: int64(n), Phone: fmt.Sprintf("+179%s%02d", suffix, n), FirstName: name, LastName: "Audit" + suffix})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return u
	}
	alice, bob := newUser("Alice", 1), newUser("Bob", 2)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM message_deletions WHERE actor_user_id = ANY($1::bigint[]) OR owner_user_id = ANY($1::bigint[])`, []int64{alice.ID, bob.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM channels WHERE creator_user_id = $1`, alice.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = ANY($1::bigint[])`, []int64{alice.ID, bob.ID})
	})

	messages := postgres.NewMessageStore(pool)
	base := int(time.Now().Unix()) - 3600
	random := time.Now().UnixNano()
	send := func(from, to domain.User, body string, at int) domain.SendPrivateTextResult {
		t.Helper()
		random++
		res, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: from.ID, RecipientUserID: to.ID, RandomID: random, Message: body, Date: at})
		if err != nil {
			t.Fatalf("send %q: %v", body, err)
		}
		return res
	}
	hi := send(alice, bob, "hi bob", base)
	hello := send(bob, alice, "hi alice", base+1)
	album1 := send(bob, alice, "holiday pics", base+2)
	album2 := send(bob, alice, "second shot", base+2)
	reply := send(bob, alice, "re: hi", base+3)
	secret := send(alice, bob, "secret plan", base+4)

	// Patch the album and the reply onto alice's copies.
	photoJSON := func(id int64) string {
		return fmt.Sprintf(`{"kind":"photo","photo":{"id":%d,"sizes":[{"kind":"size","type":"m","w":320,"h":240},{"kind":"size","type":"x","w":800,"h":600}]}}`, id)
	}
	for i, m := range []domain.SendPrivateTextResult{album1, album2} {
		if _, err := pool.Exec(ctx, `UPDATE message_boxes SET media = $3::jsonb, grouped_id = 77 WHERE owner_user_id = $1 AND box_id = $2`,
			alice.ID, m.RecipientMessage.ID, photoJSON(9_007_199_254_740_993+int64(i))); err != nil {
			t.Fatalf("patch album: %v", err)
		}
	}
	videoJSON := `{"kind":"document","document":{"id":9007199254740999,"mime_type":"video/mp4","size":2048,"attributes":[{"kind":"video","w":640,"h":360,"duration":12.5},{"kind":"filename","file_name":"clip.mp4"}],"thumbs":[{"kind":"size","type":"m","w":320,"h":180}]}}`
	if _, err := pool.Exec(ctx, `UPDATE message_boxes SET reply_to_msg_id = $3, media = $4::jsonb WHERE owner_user_id = $1 AND box_id = $2`,
		alice.ID, reply.RecipientMessage.ID, hi.SenderMessage.ID, videoJSON); err != nil {
		t.Fatalf("patch reply: %v", err)
	}
	if _, err := messages.DeleteMessages(ctx, domain.DeleteMessagesRequest{OwnerUserID: alice.ID, IDs: []int{secret.SenderMessage.ID}, Date: base + 5}); err != nil {
		t.Fatalf("delete secret: %v", err)
	}

	channels := postgres.NewChannelStore(pool)
	group, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: alice.ID, Title: "Audit Group " + suffix, Megagroup: true, MemberUserIDs: []int64{bob.ID}, Date: base})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	broadcast, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: alice.ID, Title: "Broadcast " + suffix, Broadcast: true, Date: base})
	if err != nil {
		t.Fatalf("create broadcast: %v", err)
	}
	random++
	bobInGroup, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: bob.ID, ChannelID: group.Channel.ID, RandomID: random, Message: "group hello", Date: base + 10})
	if err != nil {
		t.Fatalf("group send: %v", err)
	}
	random++
	if _, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: alice.ID, ChannelID: group.Channel.ID, RandomID: random, Message: "admin note", Date: base + 11}); err != nil {
		t.Fatalf("group send: %v", err)
	}

	t.Run("dialogs list every chat of the account, newest first", func(t *testing.T) {
		rows, err := store.ListAuditDialogs(ctx, AuditDialogQuery{OwnerUserID: alice.ID, Limit: 50})
		if err != nil {
			t.Fatalf("ListAuditDialogs: %v", err)
		}
		byPeer := map[string]AuditDialogRow{}
		for _, row := range rows {
			byPeer[fmt.Sprintf("%s:%d", row.PeerType, row.PeerID)] = row
		}
		private, ok := byPeer[fmt.Sprintf("user:%d", bob.ID)]
		if !ok || private.Kind != "private" || private.Title != "Bob Audit"+suffix || !private.HasDeletions {
			t.Fatalf("private dialog = %+v (ok=%v), want Bob with deletions", private, ok)
		}
		if private.Preview == "" || private.TopDate == 0 {
			t.Fatalf("private dialog = %+v, want a preview and a date", private)
		}
		groupRow, ok := byPeer[fmt.Sprintf("channel:%d", group.Channel.ID)]
		if !ok || groupRow.Kind != "group" || groupRow.Preview != "admin note" || !groupRow.PreviewOut || groupRow.HasDeletions {
			t.Fatalf("group dialog = %+v (ok=%v), want the group's last message, sent by alice", groupRow, ok)
		}
		// A new channel's only message is the creation service message: the
		// preview must say so, not pass it off as an empty message from the owner.
		if row, ok := byPeer[fmt.Sprintf("channel:%d", broadcast.Channel.ID)]; !ok || row.Kind != "channel" || row.PreviewMedia != "service" || row.PreviewOut {
			t.Fatalf("broadcast dialog = %+v (ok=%v), want a service-message preview", row, ok)
		}
		for i := 1; i < len(rows); i++ {
			if rows[i].TopDate > rows[i-1].TopDate {
				t.Fatalf("rows not newest first: %+v", rows)
			}
		}
	})

	t.Run("dialogs filter by kind and by name", func(t *testing.T) {
		rows, err := store.ListAuditDialogs(ctx, AuditDialogQuery{OwnerUserID: alice.ID, Kind: "private", Limit: 50})
		if err != nil || len(rows) != 1 || rows[0].PeerID != bob.ID {
			t.Fatalf("private only = %+v err %v", rows, err)
		}
		rows, err = store.ListAuditDialogs(ctx, AuditDialogQuery{OwnerUserID: alice.ID, Q: "broadcast " + suffix, Limit: 50})
		if err != nil || len(rows) != 1 || rows[0].PeerID != broadcast.Channel.ID {
			t.Fatalf("name search = %+v err %v", rows, err)
		}
	})

	t.Run("history shows deleted messages, albums, replies and media", func(t *testing.T) {
		page, err := store.AuditHistory(ctx, AuditHistoryQuery{OwnerUserID: alice.ID, PeerType: "user", PeerID: bob.ID, Limit: 50})
		if err != nil {
			t.Fatalf("AuditHistory: %v", err)
		}
		if len(page.Messages) != 6 || page.HasOlder || page.HasNewer {
			t.Fatalf("history = %+v, want all 6 copies and no more pages", page)
		}
		for i := 1; i < len(page.Messages); i++ {
			if page.Messages[i].ID <= page.Messages[i-1].ID {
				t.Fatalf("history not oldest first: %+v", page.Messages)
			}
		}
		byID := map[int]AuditMessage{}
		for _, m := range page.Messages {
			byID[m.ID] = m
		}
		if m := byID[secret.SenderMessage.ID]; !m.Deleted || !m.Out || m.Body != "secret plan" {
			t.Fatalf("deleted message = %+v, want alice's own copy, still readable", m)
		}
		if m := byID[hello.RecipientMessage.ID]; m.Out || m.SenderUserID != bob.ID {
			t.Fatalf("incoming = %+v", m)
		}
		a1, a2 := byID[album1.RecipientMessage.ID], byID[album2.RecipientMessage.ID]
		if a1.GroupedID == "" || a1.GroupedID != a2.GroupedID || a1.Media == nil || a1.Media.Kind != "photo" ||
			a1.Media.PhotoID != "9007199254740993" || a1.Media.W != 800 || a1.Media.H != 600 {
			t.Fatalf("album = %+v / %+v, want two photos sharing a group, ids as strings", a1, a2)
		}
		r := byID[reply.RecipientMessage.ID]
		if r.ReplyToID != hi.SenderMessage.ID || r.Media == nil || r.Media.Kind != "video" || r.Media.DocumentID != "9007199254740999" ||
			r.Media.Duration != 12 || r.Media.FileName != "clip.mp4" || r.Media.ThumbType != "m" || r.Media.W != 640 || r.Media.MimeType != "video/mp4" {
			t.Fatalf("reply = %+v media %+v", r, r.Media)
		}
		if page.Users[fmt.Sprint(bob.ID)].Name != "Bob Audit"+suffix || page.Users[fmt.Sprint(alice.ID)].Name != "Alice Audit"+suffix {
			t.Fatalf("users = %+v, want both participants named", page.Users)
		}
	})

	t.Run("history pages backwards and opens around a message", func(t *testing.T) {
		latest, err := store.AuditHistory(ctx, AuditHistoryQuery{OwnerUserID: alice.ID, PeerType: "user", PeerID: bob.ID, Limit: 2})
		if err != nil || len(latest.Messages) != 2 || !latest.HasOlder || latest.HasNewer || latest.Messages[1].ID != secret.SenderMessage.ID {
			t.Fatalf("latest page = %+v err %v", latest, err)
		}
		older, err := store.AuditHistory(ctx, AuditHistoryQuery{OwnerUserID: alice.ID, PeerType: "user", PeerID: bob.ID, BeforeID: latest.Messages[0].ID, Limit: 2})
		if err != nil || len(older.Messages) != 2 || older.Messages[1].ID >= latest.Messages[0].ID || !older.HasNewer {
			t.Fatalf("older page = %+v err %v", older, err)
		}
		around, err := store.AuditHistory(ctx, AuditHistoryQuery{OwnerUserID: alice.ID, PeerType: "user", PeerID: bob.ID, AroundID: album1.RecipientMessage.ID, Limit: 3})
		if err != nil || len(around.Messages) != 3 || !around.HasOlder || !around.HasNewer {
			t.Fatalf("around page = %+v err %v", around, err)
		}
		found := false
		for _, m := range around.Messages {
			found = found || m.ID == album1.RecipientMessage.ID
		}
		if !found {
			t.Fatalf("around page = %+v, want the target inside", around)
		}
	})

	t.Run("group history marks the viewing account's own messages", func(t *testing.T) {
		page, err := store.AuditHistory(ctx, AuditHistoryQuery{OwnerUserID: alice.ID, PeerType: "channel", PeerID: group.Channel.ID, Limit: 50})
		if err != nil {
			t.Fatalf("AuditHistory group: %v", err)
		}
		var bobMsg, aliceMsg *AuditMessage
		for i := range page.Messages {
			switch page.Messages[i].Body {
			case "group hello":
				bobMsg = &page.Messages[i]
			case "admin note":
				aliceMsg = &page.Messages[i]
			}
		}
		if bobMsg == nil || aliceMsg == nil || bobMsg.Out || !aliceMsg.Out || bobMsg.ID != bobInGroup.Message.ID {
			t.Fatalf("group history = %+v", page.Messages)
		}
		if page.Users[fmt.Sprint(bob.ID)].Name == "" {
			t.Fatalf("users = %+v, want the group sender named", page.Users)
		}
	})

	t.Run("search finds deleted text and filters by sender", func(t *testing.T) {
		hits, err := store.AuditSearch(ctx, AuditSearchQuery{OwnerUserID: alice.ID, PeerType: "user", PeerID: bob.ID, Q: "SECRET", Limit: 20})
		if err != nil || len(hits) != 1 || hits[0].ID != secret.SenderMessage.ID || !hits[0].Deleted {
			t.Fatalf("search = %+v err %v, want the deleted message", hits, err)
		}
		hits, err = store.AuditSearch(ctx, AuditSearchQuery{OwnerUserID: alice.ID, PeerType: "user", PeerID: bob.ID, Q: "hi a", SenderUserID: bob.ID, Limit: 20})
		if err != nil || len(hits) != 1 || hits[0].ID != hello.RecipientMessage.ID {
			t.Fatalf("search by sender = %+v err %v", hits, err)
		}
		hits, err = store.AuditSearch(ctx, AuditSearchQuery{OwnerUserID: alice.ID, PeerType: "user", PeerID: bob.ID, Q: "%", Limit: 20})
		if err != nil || len(hits) != 0 {
			t.Fatalf("search %% = %+v err %v, want it taken literally", hits, err)
		}
	})

	t.Run("deletions narrow to a page's message id range", func(t *testing.T) {
		rows, err := store.ListMessageDeletions(ctx, MessageDeletionQuery{
			OwnerUserID: alice.ID, PeerType: "user", PeerID: bob.ID,
			MinMessageID: secret.SenderMessage.ID, MaxMessageID: secret.SenderMessage.ID, Limit: 10,
		})
		if err != nil || len(rows) != 1 || rows[0].MessageID != secret.SenderMessage.ID {
			t.Fatalf("range = %+v err %v", rows, err)
		}
		rows, err = store.ListMessageDeletions(ctx, MessageDeletionQuery{
			OwnerUserID: alice.ID, PeerType: "user", PeerID: bob.ID, MaxMessageID: secret.SenderMessage.ID - 1, Limit: 10,
		})
		if err != nil || len(rows) != 0 {
			t.Fatalf("range below = %+v err %v", rows, err)
		}
	})
}
